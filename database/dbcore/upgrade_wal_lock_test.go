package dbcore

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// The helper must be a separate process: SQLite's POSIX locks are per-process,
// so opening a second connection in this test process cannot model a reader.
func TestUpgradeBackupExternalSQLiteReader(t *testing.T) {
	path := os.Getenv("LITE_TEST_UPGRADE_READER_DB")
	if path == "" {
		return
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?_busy_timeout=50")
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM lock_test").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("LITE_TEST_UPGRADE_CHECKPOINT") == "1" {
		if _, err := db.Exec("INSERT INTO lock_test (value) VALUES ('external-writer')"); err != nil {
			t.Fatal(err)
		}
		var busy, logFrames, checkpointed int
		if err := db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
			t.Fatal(err)
		}
		if busy != 1 {
			t.Fatalf("backup dropped the active reader's SHM lock: checkpoint busy=%d", busy)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeBackupPreservesWALLocks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("investigation covers Linux POSIX SQLite locks")
	}
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
	db.SetMaxIdleConns(1)
	defer db.Close()
	for _, query := range []string{
		"CREATE TABLE lock_test (id INTEGER PRIMARY KEY, value TEXT)",
		"INSERT INTO lock_test (value) VALUES ('committed')",
		"PRAGMA wal_checkpoint(TRUNCATE)",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	walBefore, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	runReader := func() {
		cmd := exec.Command(os.Args[0], "-test.run=^TestUpgradeBackupExternalSQLiteReader$")
		cmd.Env = append(os.Environ(), "LITE_TEST_UPGRADE_READER_DB="+path)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("external reader: %v\n%s", err, output)
		}
	}
	// A normal independent read/close must not unlink the live parent's WAL.
	runReader()
	walControl, err := os.Stat(path + "-wal")
	if err != nil || !os.SameFile(walBefore, walControl) {
		t.Fatalf("control reader unexpectedly changed live WAL: %v", err)
	}
	// Upgrades must snapshot SQLite without dropping the live connection's locks.
	if err := createUpgradeBackup(db, data, filepath.Join(root, "upgrade.zip"), nil); err != nil {
		t.Fatal(err)
	}
	runReader()
	walAfter, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatalf("upgrade backup released live SQLite locks: external reader removed WAL: %v", err)
	}
	if !os.SameFile(walBefore, walAfter) {
		t.Fatal("upgrade backup let external reader replace live WAL")
	}
}

func TestUpgradeBackupPreservesWALReadLocks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("checks Linux POSIX SHM read locks")
	}
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
	// Two connections let a read transaction remain active while the backup
	// uses another connection; all POSIX locks still belong to this process.
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	defer db.Close()
	for _, query := range []string{
		"PRAGMA wal_autocheckpoint=0",
		"CREATE TABLE lock_test (id INTEGER PRIMARY KEY, value TEXT)",
		"INSERT INTO lock_test (value) VALUES ('reader-snapshot')",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	reader, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.ExecContext(ctx, "BEGIN DEFERRED"); err != nil {
		t.Fatal(err)
	}
	defer reader.ExecContext(ctx, "ROLLBACK")
	var count int
	if err := reader.QueryRowContext(ctx, "SELECT count(*) FROM lock_test").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := createUpgradeBackup(db, data, filepath.Join(root, "upgrade.zip"), nil); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestUpgradeBackupExternalSQLiteReader$")
	cmd.Env = append(os.Environ(), "LITE_TEST_UPGRADE_READER_DB="+path, "LITE_TEST_UPGRADE_CHECKPOINT=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("external checkpoint bypassed active SHM reader lock: %v\n%s", err, output)
	}
}
