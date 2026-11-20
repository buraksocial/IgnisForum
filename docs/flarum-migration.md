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
