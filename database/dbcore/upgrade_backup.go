package dbcore

import (
	"archive/zip"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// createUpgradeBackup must never open the active main database or its side
// files as ordinary os.Files. On POSIX, closing any descriptor for an inode
// releases that process's fcntl locks, including locks held by SQLite on a
// different descriptor. An independent SQLite connection could then unlink
// WAL/SHM while the server still uses them.
func createUpgradeBackup(db *sql.DB, dataDir, archivePath string, exclude map[string]struct{}) error {
	if db == nil {
		return fmt.Errorf("main SQLite connection is unavailable")
	}
	mainPath, err := mainSQLitePath(db)
	if err != nil {
		return err
	}
	// Restore already recognizes a root lite.db entry, including custom or
	// symlinked live database paths. Physical paths are only used for exclusion.
	archiveName := "lite.db"
	if err := rejectSQLiteInstanceKeyAlias(dataDir, mainPath); err != nil {
		return err
	}

	snapshotDir, err := os.MkdirTemp("", "lite-upgrade-snapshot-")
	if err != nil {
		return fmt.Errorf("create SQLite snapshot directory: %w", err)
	}
	defer os.RemoveAll(snapshotDir)
	snapshot := filepath.Join(snapshotDir, "lite.db")
	// VACUUM INTO includes committed WAL data without checkpointing or opening
	// the live database through Go's ordinary file APIs.
	if _, err := db.Exec("VACUUM INTO ?", snapshot); err != nil {
		return fmt.Errorf("snapshot main SQLite database: %w", err)
	}

	skip := make(map[string]struct{}, len(exclude)+4)
	for path := range exclude {
		skip[path] = struct{}{}
	}
	if mainPath != "" {
		for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
			skip[mainPath+suffix] = struct{}{}
		}
	}
	// Do not combine a replacement root snapshot with stale sidefiles from a
	// previous/default database when the active database lives elsewhere.
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		skip[filepath.Join(dataDir, archiveName)+suffix] = struct{}{}
	}
	return zipDirectoryWithFiles(dataDir, archivePath, skip, map[string]string{archiveName: snapshot})
}

// The key is read separately from the directory walk, so it needs the same
// pre-open inode guard. Rejecting invalid key bytes after ReadFile is too late.
func rejectSQLiteInstanceKeyAlias(dataDir, mainPath string) error {
	if mainPath == "" {
		return nil
	}
	keyPath, _, err := restoreKeyPlan(dataDir)
	if err != nil {
		return err
	}
	keyInfo, err := os.Stat(keyPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect instance key before backup: %w", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if live, err := os.Stat(mainPath + suffix); err == nil && os.SameFile(keyInfo, live) {
			return fmt.Errorf("instance key aliases an active SQLite file")
		}
	}
	return nil
}

func mainSQLitePath(db *sql.DB) (string, error) {
	rows, err := db.Query("PRAGMA database_list")
	if err != nil {
		return "", fmt.Errorf("inspect main SQLite database: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sequence int
		var name, path string
		if err := rows.Scan(&sequence, &name, &path); err != nil {
			return "", err
		}
		if name == "main" {
			return path, nil
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("main SQLite database was not found")
}

func writeZipFile(writer *zip.Writer, name, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	entry, err := writer.Create(name)
	if err != nil {
		file.Close()
		return err
	}
	_, copyErr := io.Copy(entry, file)
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func writeExtraZipFiles(writer *zip.Writer, files map[string]string) error {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := writeZipFile(writer, name, files[name]); err != nil {
			return err
		}
	}
	return nil
}
