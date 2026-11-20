# IgnisForum production-completion roadmap

## Goal

Replace the current Flarum service at `forum.tutorial.com.tr` only after IgnisForum
has feature coverage, migrated data, verified recovery, and a rehearsed rollback.
The old forum and its MariaDB data remain authoritative until cutover acceptance.

## Workstreams

- [x] Harden the standalone service: loopback-only bind, unprivileged systemd unit,
  HTTPS-only cookies, explicit timeouts, and remove the published demo password.
- [ ] Complete forum administration: user/group management, editable
  global and node permissions, roles, moderation queue, reports, and audit trail.
- [x] Add protected node/category/forum creation, editing, reparenting, and
  empty-only deletion with hierarchy and content-preservation checks.
- [ ] Complete core community workflows: edit/delete/quote, thread prefixes and
  tags, polls, attachments, profiles, notifications, private messages, and follows.
- [ ] Add safe theme support: theme manifests, parent/child themes, per-user
  preference, preview/rollback, and cache invalidation without editing templates.
- [ ] Add an extension system with a versioned, capability-limited API, lifecycle
  hooks, configuration, dependency/version checks, and enable/disable rollback.
- [x] Implement the initial no-import WASM runtime, bounded `home.decorate` hook,
  exact-version dependency validation, and operator-installed package format.
- [ ] Add admin-managed installation/preview/configuration, individual plugin
  enable/disable, audit events, and rollback before treating extensions as complete.
- [ ] Build a read-only Flarum importer for users, groups, discussions, posts,
  reactions, tags, and uploads; retain source IDs and produce reconciliation reports.
- [ ] Rehearse backup/restore, migration, load/race/security tests, and rollback;
  then perform a controlled proxy cutover and verify the public domain.

## Release gates

1. No public listener on the application port; TLS terminates at the trusted proxy.
2. Admin bootstrap credentials are unique and recoverable without plaintext storage.
3. Every mutation is authorized, CSRF protected, rate limited where appropriate,
   and covered by integration tests.
4. Primary records rebuild every derived index; restore tests prove recoverability.
5. Flarum source counts and imported counts reconcile exactly, with an exception
   report for records that cannot be represented.
6. The cutover is reversible and the original Flarum data is untouched until the
   user explicitly approves decommissioning it.
