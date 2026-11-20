# Automatic update requirements (not yet enabled)

Do not run an updater in the web service or install directly from a moving Git
branch. Publish immutable artifacts built against a pinned IgnisDB-Go revision
and sign release metadata with a separately managed release key. Metadata binds
version, channel, platform, artifact size and hash, expiry, and supported data
format ranges. Stable and security channels must be explicitly opted into;
never silently downgrade or accept expired or unsigned metadata.

An operator-owned updater should stage verified artifacts outside the serving
tree, rehearse restore and migration using an isolated verified backup, then
run health and authenticated smoke tests before promotion. Keep the previous
binary/config with that backup. Roll back the binary only if the old binary
can read the resulting data; otherwise restore under a write freeze so that
no accepted writes are silently lost. Log verification, promotion and recovery
results. Start in check-only mode until all stages pass a production rehearsal.

## Local check-only tool

Build with `go build ./cmd/ignisforum-update-check`. Use a public key
provisioned independently of the release files:

```sh
./ignisforum-update-check -channel stable -installed 1.2.3 \
  -public-key /etc/ignisforum/release-key.b64 \
  -metadata release.json -signature release.sig -artifact ./ignisforum-staged
```

The detached Ed25519 signature covers the exact bytes of `release.json`;
the key and signature files use standard base64. Schema 1 requires `schema`
(1), `product` (`IgnisForum`), `channel` (`stable` or `security`), `version`
(numeric `MAJOR.MINOR.PATCH`), `platform` (e.g. `linux/amd64`), `size` (bytes),
`sha256` (lowercase hex), and `issued`/`expires` (RFC 3339; max 30-day validity).
The installed version must come from the installed release, not the feed.
Local mode does not use the network. Without `-artifact`, it verifies metadata
only. It does not check database compatibility or authorize promotion.

## Scheduled HTTPS checks (opt-in, no installation)

An operator may publish a platform-specific JSON feed containing two strings:
`metadata` (the standard base64 encoding of the exact signed `release.json`
bytes) and `signature` (the standard base64 detached Ed25519 signature). There
is no artifact URL in the feed. Serve the document directly over HTTPS with a
trusted certificate; redirects and compressed responses are not accepted.
Republish fresh signed metadata before its expiry even when the version stays
the same. Install the trusted public key out of band, not from the feed.

First check a release manually on an isolated instance:

```sh
./ignisforum-update-check -channel stable -installed 1.2.3 \
  -public-key /etc/ignisforum/release-stable-key.b64 \
  -feed https://releases.example.invalid/ignisforum/stable/linux-amd64.json
```

A valid newer release reports `update_available: true`; valid unchanged or
older metadata reports `false`. Invalid, expired or unavailable metadata
fails the check instead. All feed results have `artifact_verified: false`.

For unattended checks, place the checker at
`/opt/ignisforum/bin/ignisforum-update-check` and install the two template units
from `deploy/` into `/etc/systemd/system/`. Copy `deploy/update-feed.env.example`
to `/etc/ignisforum/update-stable.env` or `/etc/ignisforum/update-security.env`
and fill in the actual installed release version, provisioned key path and
feed URL. Keep the environment file root-owned and the **public** key readable
by the checker's dynamic service user. Update `INSTALLED_VERSION` on every
promotion or rollback; a development build cannot be used as that version.
After `systemctl daemon-reload`, start the matching service instance once and
check its journal before enabling the corresponding timer (e.g.
`systemctl enable --now ignisforum-update-check@stable.timer`). Stable and
security instances are independent. The timer runs daily with a randomized
delay and catches up after downtime. No live feed, keys or enabled timers are
provided here. The checker never downloads an artifact or restarts the forum.

## Explicit artifact staging (no installation)

Build the separate tool with `go build ./cmd/ignisforum-update-stage`. Unlike the
checker, it downloads an artifact, but only when explicitly invoked with an
operator-selected URL, trusted key, signed metadata and a new staging directory.
The feed still cannot nominate an artifact URL. The following versions and
addresses are examples, not a published release:

```sh
install -d -m 0700 "$HOME/ignisforum-stage"
./ignisforum-update-stage -channel stable -installed 1.2.3 \
  -public-key /etc/ignisforum/release-stable-key.b64 \
  -metadata release.json -signature release.sig \
  -artifact-url https://releases.example.invalid/ignisforum/1.2.4/linux-amd64 \
  -stage-dir "$HOME/ignisforum-stage/stable-1.2.4"
```

Use an operator-owned private directory outside the serving tree and never run
the command as the web-service user. The destination must not exist, and its
parent must exist with no group/other permissions and no symlink components.
The parent and its ancestors must be under trusted operator control; do not
share it with processes that can replace directories during staging. The tool
does not change permissions on pre-existing paths or overwrite previous stages.

Both channels are supported independently. Metadata is verified before any
network access. The download uses direct HTTPS with normal certificate
verification, no environment proxy, redirects, cookie jar or compression, a
two-minute timeout and the signed size bound (at most 256 MiB). Size and SHA-256
are verified while streaming, and expiry is checked again after the transfer
against the current clock, not a caller-provided historical timestamp.

Success produces a 0700 directory containing four 0600 files: `artifact`, the
exact signed `release.json`, a base64 `release.sig`, and `ready.json`. Files and
directory entries are synchronized before success is reported. The result has
`artifact_verified: true` and `installed: false`. There is no extraction, execute
permission, package script, service restart, production-database access or
automatic promotion. Failed attempts try to remove only files they created;
an interrupted process may leave an incomplete directory that must be reviewed
and removed by the operator before retrying. There is no resume or automatic
retention cleanup.

`ready.json` is an unsigned completion receipt, not an installation permit.
Reverify the metadata/signature/artifact using the independently provisioned
public key immediately before any future promotion; a staged artifact can be
modified or expire afterwards. Schema 1 still does not express database-format
compatibility. Publishing signed releases and their trust roots, compatibility
gates, isolated backup/restore and migration rehearsal, authenticated health
tests, atomic promotion and rollback remain required before automatic
installation can be enabled. No staging timer or live installation is enabled
by this change.
