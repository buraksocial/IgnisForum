# Flarum migration snapshots

`tools/flarum_snapshot.php` produces the source-side input for a staged import.
It opens a repeatable-read, read-only transaction, reads an explicit allowlist
of migration tables, and writes a versioned JSON snapshot. It does not update
Flarum, its schema, or its records.

The snapshot contains account email addresses and password hashes, forum text,
notification payloads, and moderation data. It contains no plaintext passwords
and does not export Flarum login/API/reset tokens. The output is created with
mode `0600`; the target must not already exist. Keep the file in a private
directory and remove it after migration and acceptance no longer need it.

Example (run as an account that can read Flarum config and connect to MariaDB):

```sh
install -d -m 0700 /var/lib/ignisforum/migration
/opt/plesk/php/8.3/bin/php \
  /var/www/vhosts/tutorial.com.tr/IgnisForum/tools/flarum_snapshot.php \
  /var/www/vhosts/tutorial.com.tr/forum.tutorial.com.tr/config.php \
  /var/lib/ignisforum/migration/flarum-snapshot.json
```

The exporter reports row counts only. Import acceptance must compare each
snapshot table's `source_count` with both its exported `rows` length and the
destination reconciliation report. Unsupported fields/records must be listed
as exceptions; do not treat a successful snapshot as proof that import or
cutover is complete. Never point an importer at the live IgnisForum data
directory; use a separate staging data directory and retain Flarum as the
authoritative service until restore and rollback rehearsals pass.

## Isolated staging import

Build and run the importer only against a new private staging directory and a
new report path. The importer rejects overlap with the configured production
data directory, refuses a non-empty store without its snapshot marker, and
rejects a different snapshot on replay. A same-snapshot replay is supported
after an interrupted import.

```sh
install -d -m 0700 /var/lib/ignisforum/migration/staging
go run ./cmd/flarum-import \
  -config /etc/ignisforum/config.yaml \
  -snapshot /var/lib/ignisforum/migration/flarum-snapshot.json \
  -data-dir /var/lib/ignisforum/migration/staging \
  -report /var/lib/ignisforum/migration/reconciliation.json
```

Review every table's source/imported counts and every exception before accepting
the staging copy. The importer preserves source IDs for users, tags/forums,
discussions, and posts, strips active HTML from rendered post content, and maps
Flarum `viewForum`, `startDiscussion`, and `discussion.reply` grants to the
corresponding IgnisForum permissions. Other custom/moderation permissions,
read/subscription state, notifications, mention relations, and some Flarum
visibility semantics do not have equivalent IgnisForum features; their omission
must be resolved or explicitly accepted.
The report is private because it may contain source identifiers and moderation
details. This command does not switch traffic or change Flarum.

If the source Flarum avatar directory is available, pass it read-only to the
importer; validated PNG/JPEG/GIF files are copied into the isolated staging
data directory and referenced by generated, non-user-controlled filenames:

```sh
go run ./cmd/flarum-import -config /etc/ignisforum/config.yaml \
  -snapshot /var/lib/ignisforum/migration/flarum-snapshot.json \
  -data-dir /var/lib/ignisforum/migration/staging-with-avatars \
  -avatar-source /var/www/vhosts/tutorial.com.tr/forum.tutorial.com.tr/public/assets/avatars \
  -report /var/lib/ignisforum/migration/avatar-import-report.json
```

Avatar import failures are listed as reconciliation exceptions, not silently
ignored. IgnisForum serves these files from its configured private avatar
directory (default: `<data_dir>/uploads/avatars`) with fixed image content
types and `nosniff` protection. The offline backup/restore format includes
this avatar directory.

## Offline backup and restore rehearsal

The engine is single-writer and stores primary records in SSTables/WAL; forum
indexes are rebuilt from primary records at startup. A consistent filesystem
backup therefore requires a clean stop. The backup tool refuses a live store
lock, archives only recognized regular database files, writes mode-`0600`
artifacts with a SHA-256 manifest, and restores only safe paths into a new
directory. Restore runs a database open/rebuild/close verification before it
reports success.

Do this first on staging. Do not run a restore over the configured production
data directory; restore destinations must be new and isolated.

```sh
install -d -m 0700 /var/lib/ignisforum/backups
go run ./cmd/ignisforum-backup -config /etc/ignisforum/config.yaml \
  -action backup -archive /var/lib/ignisforum/backups/staging.tar.gz \
  -data-dir /var/lib/ignisforum/migration/staging

go run ./cmd/ignisforum-backup -config /etc/ignisforum/config.yaml \
  -action restore -archive /var/lib/ignisforum/backups/staging.tar.gz \
  -restore-dir /var/lib/ignisforum/migration/restore-check
```

On production, stop the IgnisForum service only during an approved maintenance
window; the public domain currently remains on Flarum. Keep the archive and its
JSON manifest in private storage, and test that both are readable before
relying on them for rollback.
