# Production deployment

IgnisForum must run as an unprivileged service on loopback behind a TLS reverse
proxy. Do not bind the Go process to a public interface or cache authenticated
responses at the proxy/CDN.

## Install

1. Build the embedded Rust engine and Go binary with `make build`.
2. Install the binary at `/opt/ignisforum/bin/ignisforum`.
3. Create the `ignisforum` system account and `/var/lib/ignisforum/data` data dir.
4. Copy `production.yaml.example` to `/etc/ignisforum/config.yaml`, set owner
   `root:ignisforum`, mode `0640`, and review every value.
5. Install `ignisforum.service` in `/etc/systemd/system/`, run `systemctl daemon-reload`,
   then enable and start the unit.
6. Configure the TLS reverse proxy to `http://127.0.0.1:8090`, preserve the
   original host, and pass a proxy-generated client IP. Do not trust client
   supplied forwarding headers from the public network.

Themes and plugins are operator-installed under their configured directories.
See `plugins.md` for the current WASM API and its deliberately limited v1 hooks.

## Administrator recovery

The published demo password is never installed by normal server startup.
If upgrading a seeded development database, the known demo administrator is
locked automatically. Recover it locally through stdin, not a shell argument:

```sh
umask 077
read -r -s -p 'New administrator password: ' password
printf '\n'
printf '%s\n' "$password" | sudo -u ignisforum \
  /opt/ignisforum/bin/ignisforum --config=/etc/ignisforum/config.yaml \
  --reset-admin-password
unset password
```

Use a unique password of at least 16 characters. The command writes only its
Argon2id hash and never echoes or logs the plaintext.

## Backups and cutover

Stop the service cleanly before making a filesystem-level copy of the embedded
database. Keep an off-host encrypted backup and verify a restore into a separate
data directory before changing production routing. Keep the existing forum
available until migration counts, login, permissions, search, uploads, and
rollback have all been acceptance-tested.
