# SQLite upgrade-backup lock safety

## Confirmed defect and scope

The upgrade auto-backup previously checkpointed the main database and then
archived `data/` with ordinary `os.Open` / `Close` calls while the SQLite
connection was still open. On Linux/POSIX, closing any descriptor for a file
releases the process's `fcntl` locks on that inode, including locks owned by
SQLite through another descriptor. Reading the active SHM file has the same
problem. See SQLite's [corruption/locking guidance](https://sqlite.org/howtocorrupt.html).

An independent read-write SQLite connection can then checkpoint and remove
WAL/SHM on close while the server still holds their old, unlinked descriptors.
Later connections can see a different WAL state for the same main database.
This makes a passing integrity check on a separate connection insufficient to
establish that the server's active connection sees the same database.

Linux subprocess regressions reproduce two distinct failures in the old path:

- A separate connection's normal read/close removes the live server's WAL.
- A separate writer can truncate WAL despite an active server read transaction,
  because the raw backup has dropped the server's SHM read locks.

This is an inherited upgrade-backup defect, not a new low-level index-writing
operation in MCP grant editing. The tests prove the lock-loss mechanism; they
do not uniquely establish the historical cause of any production incident or
claim to reproduce every possible SQLite corruption error.

## Safe backup behavior

Upgrade backup now obtains a standalone SQLite snapshot with `VACUUM INTO`
through the existing connection. It archives that snapshot as root `lite.db`,
which the restore code already recognizes, while preserving other data files
and the existing instance-key handling.

The archive walk must not open the active main database, its WAL/SHM/journal,
or soft/hard-link aliases. Sidefiles corresponding to the replacement archive
entry are also excluded, even when the active database lives outside `data/`.
An instance-key path that aliases an active SQLite file is rejected before it
is read. Snapshot failures do not advance the version marker or prune previous
upgrade backups.

The automatic backup runs during startup, before the metrics store is opened;
this change snapshots the already-open main database. It is not a general
replacement for arbitrary hot backups of every database in `data/`.

## Regression checks

```sh
go test ./database/dbcore ./web/mcp ./web/api/admin ./web/backup ./utils/instancekey ./pkg/migrations
go test ./database/dbcore -run '^TestUpgradeBackup' -count=20
go test ./web/mcp -run '^TestSaveLeasePolicyPreservesFileWALIndexes$' -count=10
go test -race ./database/dbcore ./web/mcp
go build ./...
```

Coverage includes subprocess main/SHM lock preservation, committed data still
in WAL, standalone snapshot integrity, root restore layout for nested/external/
symlinked databases, source aliases, instance-key aliases, failed snapshots,
legacy `all_nodes` migration, repeated policy transitions, refresh-token expiry,
and closing/reopening file-backed WAL databases.

## Existing damaged databases

This prevents the confirmed unsafe backup path. It does not silently reindex
or replace an already-damaged running database. If a process holds deleted
WAL/SHM descriptors, preserve its committed WAL state before stopping it;
simply deleting sidefiles or restarting can lose recent data. Recover and
validate a consistent snapshot offline, then reopen the service's connections
and verify the actual failing request separately.
