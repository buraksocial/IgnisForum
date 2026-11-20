# IgnisForum production-completion roadmap

## Goal

Replace the current Flarum service at `forum.tutorial.com.tr` only after IgnisForum
has feature coverage, migrated data, verified recovery, and a rehearsed rollback.
The old forum and its MariaDB data remain authoritative until cutover acceptance.

## Workstreams

- [x] Harden the standalone service: loopback-only bind, unprivileged systemd unit,
  HTTPS-only cookies, explicit timeouts, and remove the published demo password.
- [ ] Complete forum administration: user/group management, roles, moderation
  actions, and audit trail; global/node permissions and report queue are shipped.
- [x] Add protected node/category/forum creation, editing, reparenting, and
  empty-only deletion with hierarchy and content-preservation checks.
- [ ] Complete core community workflows: thread prefixes, attachments,
  richer notification events, and private messages.
- [x] Add a persistent reply notification inbox for discussion owners, unread
  counts, per-item read state, and startup-rebuildable indexes.
- [x] Add optional single- or multi-choice thread polls gated by forum settings,
  with one ballot per member and vote counts restored from vote records.
- [x] Add normalized thread tags with a five-tag limit, indexed tag pages, and
  rebuildable tag/thread indexes.
- [x] Add public member profiles with recent visible posts and self-service
  title/signature editing protected by CSRF, ownership checks, and rate limits.
- [x] Add soft deletion for replies with author/admin authorization, CSRF and
  rate limits, first-post protection, index cleanup, and rebuildable counters.
- [x] Add same-thread post quoting that safely prefills the reply composer and
  rejects missing, hidden, or cross-thread source posts.
- [x] Add member thread follow/unfollow controls and persist per-thread read
  progress when an authenticated member views a page.
- [x] Add time-limited author post editing plus administrator editing, with
  CSRF/rate-limit checks and full-text index refresh.
- [x] Add member post reports, a rebuildable open-report queue, and administrator
  resolution with retained report history.
- [ ] Add safe theme support: theme manifests, parent/child themes, per-user
  preference, preview/rollback, and cache invalidation without editing templates.
- [ ] Add an extension system with a versioned, capability-limited API, lifecycle
  hooks, configuration, dependency/version checks, and enable/disable rollback.
- [x] Implement the initial no-import WASM runtime, bounded `home.decorate` hook,
  exact-version dependency validation, and operator-installed package format.
- [x] Add an administrator settings page for built-in announcement, statistics,
  presence, sponsored-link and external media modules; list installed WASM
  packages and preview/select installed themes.
- [x] Add bounded theme overrides with a private draft preview and one-step
  settings rollback independent of widget and WASM preferences.
- [x] Add opt-in local post attachments with bounded storage, permission-checked
  downloads and attachment-only responses.
- [x] Start the locale catalogs with shared navigation, home/widgets, latest,
  search, tags, notifications, 404 and external media. Keep English preview-only.
- [x] Extend catalogs to authentication forms and validation errors, forum
  listings and member profiles, retaining preview-only language activation.
- [x] Catalog thread pages, thread creation, post controls, reactions and polls;
  test English controls without translating user-authored content.
- [x] Catalog the administration dashboard, reports and permission matrix,
  preserving stored permission keys and form actions.
- [ ] Add a complete interface message catalog (including server errors and
  plugins), locale selection, localized dates/plurals and RTL coverage; verify
  at least 20 production-ready translations end to end.
- [x] Add opt-in scheduled checks of signed stable and security metadata,
  without downloading or installing releases.
- [ ] Publish signed stable and security release metadata; build an opt-in updater
  with staging verification, backup and rollback, separate from the web process.
- [x] Allow administrators to enable/disable individual WASM plugins without
  restart, with persistent settings and dependency-aware suspension.
- [ ] Add admin-managed installation/preview/configuration, durable audit events,
  and version rollback before treating extensions as complete.
- [x] Build an isolated read-only Flarum staging importer for users, groups,
  discussions, posts, reactions, and tags; retain source IDs and produce
  per-table reconciliation reports with explicit unsupported-data exceptions.
- [x] Import validated Flarum profile avatars into isolated staging, serve them
  through a constrained image route, and include them in offline backup/restore.
- [ ] Add post uploads/attachments migration and close the remaining Flarum permission,
  moderator-role, follow-notification, and visibility-semantic gaps before migration
  acceptance. `discussion_user` read/subscription records are preserved and
  surfaced in the basic member workflow. Unsupported moderator roles are
  omitted rather than downgraded to members, avoiding accidental privilege broadening.
- [x] Add a repeatable-read, read-only Flarum snapshot exporter with an explicit
  table allowlist, protected output file, and per-table source counts.
- [x] Verify Flarum PHP `$2y$` bcrypt hashes and upgrade accounts to Argon2id
  only after successful login; reject unsafe imported Argon2 parameters.
- [x] Implement checksummed offline database backup, path-safe isolated restore,
  and a staging backup/restore rehearsal with database rebuild verification.
- [ ] Rehearse backup/restore against production data, complete migration and
  load/rollback acceptance; then perform a controlled proxy cutover and verify
  the public domain.

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
