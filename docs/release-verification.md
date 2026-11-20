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
