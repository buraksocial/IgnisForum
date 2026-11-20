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
The checker does not fetch, extract, execute, replace or restart anything.
Without `-artifact`, it verifies metadata only. It does not check database
compatibility or authorize promotion. No public feed, release keys or timer
are provisioned by this change.
