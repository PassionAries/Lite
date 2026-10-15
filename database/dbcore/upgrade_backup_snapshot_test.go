package dbcore

import (
	"archive/zip"
	"database/sql"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUpgradeBackupIncludesCommittedWALAndSkipsLiveAliases(t *testing.T) {
	for _, location := range []string{"nested", "outside-data", "symlink-main"} {
		t.Run(location, func(t *testing.T) {
			root := t.TempDir()
			data := filepath.Join(root, "data")
			if err := os.MkdirAll(filepath.Join(data, "sql"), 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(data, "sql", "state.sqlite")
			archiveMain := "lite.db"
			if location == "outside-data" {
				path = filepath.Join(root, "external.sqlite")
				if err := os.WriteFile(filepath.Join(data, "lite.db"), []byte("obsolete database"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			physicalPath := path
			if location == "symlink-main" {
				if runtime.GOOS == "windows" {
					t.Skip("symlink fixture requires filesystem symlink support")
				}
				path = filepath.Join(data, "lite.db")
				if err := os.Symlink(physicalPath, path); err != nil {
					t.Fatal(err)
				}
			}
			db, err := sql.Open("sqlite3", buildSQLiteDSN(path))
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			defer db.Close()
			for _, query := range []string{
				"PRAGMA wal_autocheckpoint=0",
				"CREATE TABLE entries (id INTEGER PRIMARY KEY, value TEXT)",
				"CREATE INDEX entries_value ON entries(value)",
				"INSERT INTO entries (value) VALUES ('checkpointed')",
				"PRAGMA wal_checkpoint(TRUNCATE)",
				"INSERT INTO entries (value) VALUES ('only-in-wal')",
			} {
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			walInfo, err := os.Stat(physicalPath + "-wal")
			if err != nil || walInfo.Size() <= 32 {
				t.Fatalf("fixture has no pending WAL data: %v", err)
			}
			if runtime.GOOS != "windows" {
				for name, target := range map[string]string{"main-symlink": physicalPath, "shm-symlink": physicalPath + "-shm"} {
					if err := os.Symlink(target, filepath.Join(data, name)); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Link(physicalPath, filepath.Join(data, "main-hardlink")); err != nil {
					t.Fatal(err)
				}
			}
			metadata := filepath.Join(data, "settings.json")
			if err := os.WriteFile(metadata, []byte(`{"setting":"retained"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			importArchive := filepath.Join(data, "backup.zip")
			if err := os.WriteFile(importArchive, []byte("not included"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				if err := os.WriteFile(filepath.Join(data, "lite.db")+suffix, []byte("obsolete sidefile"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			archive := filepath.Join(root, "upgrade.zip")
			if err := createUpgradeBackup(db, data, archive, map[string]struct{}{importArchive: {}}); err != nil {
				t.Fatal(err)
			}
			reader, err := zip.OpenReader(archive)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			mainEntries, metadataEntries := 0, 0
			for _, entry := range reader.File {
				if strings.HasSuffix(entry.Name, "-wal") || strings.HasSuffix(entry.Name, "-shm") || strings.HasSuffix(entry.Name, "-journal") || strings.Contains(entry.Name, "-symlink") || entry.Name == "main-hardlink" || entry.Name == "backup.zip" {
					t.Fatalf("archived excluded live file: %s", entry.Name)
				}
				if entry.Name == "settings.json" {
					metadataEntries++
				}
				if entry.Name != archiveMain {
					continue
				}
				mainEntries++
				r, err := entry.Open()
				if err != nil {
					t.Fatal(err)
				}
				bytes, err := io.ReadAll(r)
				r.Close()
				if err != nil {
					t.Fatal(err)
				}
				extracted := filepath.Join(root, "extracted.sqlite")
				if err := os.WriteFile(extracted, bytes, 0o600); err != nil {
					t.Fatal(err)
				}
				snapshot, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(extracted)+"?mode=ro&immutable=1")
				if err != nil {
					t.Fatal(err)
				}
				var count int
				if err := snapshot.QueryRow("SELECT count(*) FROM entries").Scan(&count); err != nil || count != 2 {
					snapshot.Close()
					t.Fatalf("snapshot omitted committed WAL row: count=%d err=%v", count, err)
				}
				var check string
				if err := snapshot.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
					snapshot.Close()
					t.Fatalf("snapshot index integrity: %q %v", check, err)
				}
				snapshot.Close()
			}
			if mainEntries != 1 || metadataEntries != 1 {
				t.Fatalf("archive entry counts: main=%d metadata=%d", mainEntries, metadataEntries)
			}
		})
	}
}

func TestUpgradeBackupSnapshotFailurePreservesExistingArchive(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "upgrade.zip")
	original := "previous completed backup"
	if err := os.WriteFile(archive, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", buildSQLiteDSN(filepath.Join(data, "lite.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, connection := range []*sql.DB{nil, db} {
		if err := createUpgradeBackup(connection, data, archive, nil); err == nil {
			t.Fatal("snapshot of an unavailable connection succeeded")
		}
		bytes, err := os.ReadFile(archive)
		if err != nil || string(bytes) != original {
			t.Fatalf("existing backup changed: %q %v", bytes, err)
		}
		if _, err := os.Stat(archive + ".partial"); !os.IsNotExist(err) {
			t.Fatalf("failed snapshot published partial archive: %v", err)
		}
	}
}

func TestUpgradeBackupRejectsInstanceKeySQLiteAliases(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("checks Linux POSIX lock preservation")
	}
	for _, alias := range []string{"main", "shm", "symlink", "hardlink"} {
		t.Run(alias, func(t *testing.T) {
			root := t.TempDir()
			data := filepath.Join(root, "data")
			if err := os.Mkdir(data, 0o700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(data, "lite.db")
			db, err := sql.Open("sqlite3", buildSQLiteDSN(path))
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			defer db.Close()
			if _, err := db.Exec("CREATE TABLE lock_test (id INTEGER PRIMARY KEY, value TEXT)"); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path + "-wal")
			if err != nil {
				t.Fatal(err)
			}
			key := path
			switch alias {
			case "shm":
				key = path + "-shm"
			case "symlink":
				key = filepath.Join(root, "key-alias")
				if err := os.Symlink(path, key); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				key = filepath.Join(root, "key-alias")
				if err := os.Link(path, key); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("LITE_INSTANCE_KEY_FILE", key)
			archive := filepath.Join(root, "upgrade.zip")
			if err := createUpgradeBackup(db, data, archive, nil); err == nil || !strings.Contains(err.Error(), "aliases an active SQLite file") {
				t.Fatalf("SQLite key alias was not rejected before reading: %v", err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestUpgradeBackupExternalSQLiteReader$")
			cmd.Env = append(os.Environ(), "LITE_TEST_UPGRADE_READER_DB="+path)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("external reader: %v\n%s", err, output)
			}
			after, err := os.Stat(path + "-wal")
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("rejected key alias dropped live SQLite locks: %v", err)
			}
			if _, err := os.Stat(archive); !os.IsNotExist(err) {
				t.Fatalf("rejected key alias published a backup: %v", err)
			}
		})
	}
}
