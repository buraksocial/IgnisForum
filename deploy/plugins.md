# IgnisForum WebAssembly plugin API v1

Plugins are trusted administrator-installed packages, not downloadable code
that members can upload. Put each package in its own directory under the
configured `plugin_dir` (default production path:
`/var/lib/ignisforum/plugins/<id>/`). Restart IgnisForum to validate and load
packages. A broken package fails startup rather than being silently ignored.

Each package contains:

- `plugin.json`: UTF-8 JSON manifest (`id`, `name`, `version`, `api_version`,
  `hooks`, and optional exact-version `depends`).
- `plugin.wasm`: a WebAssembly module no larger than 8 MiB.

API v1 currently exposes only the `home.decorate` hook. Plugins receive a JSON
envelope shaped like `{"hook":"home.decorate","data":{"version":"...",
"categories":4}}` and return JSON such as `{"notices":["Welcome"]}`. Notice
text is length-limited, rendered through `html/template` escaping, and never
interpreted as HTML. At most five notices of 240 bytes each are shown.

The module must export linear memory named `memory` and these functions:

```text
ignis_plugin_api() -> i32             // return 1
ignis_alloc(i32 byte_length) -> i32   // return guest-memory pointer
ignis_hook(i32 ptr, i32 len) -> i64  // high 32 bits: output pointer;
                                      // low 32 bits: output byte length
```

The hook input is the JSON envelope above. The output pointed to by the packed
`i64` must be valid JSON. Guest memory is capped at 16 MiB, module files at 8
MiB, input/output at 64 KiB, at most 16 installed packages, and each complete
hook dispatch across all plugins at 100 ms. The runtime has no
WASI, imported host functions, filesystem, network, clock, random source, or
direct forum database access. All imports are rejected during package loading.
Dependencies require exact installed versions and are initialized in stable
dependency order; cycles and missing/mismatched versions are rejected.

The appearance page (`/admin/appearance#wasm`) lists installed WASM packages
and can enable or disable individual packages without restarting the forum.
Disabling a dependency suspends its dependents transitively without changing
their individual settings. Changes persist in the forum database and apply
to subsequent requests; a hook already running may finish. Disabled packages
are still validated at startup. Remove a broken package via the filesystem
before restarting if it fails validation.

The panel accepts an optional bounded flat JSON configuration per package: up
to 32 lowercase keys, 4096 bytes total, and only short strings, booleans or
integers between -1000000 and 1000000. Nested values, nulls, arrays, duplicate
keys, decimal numbers and trailing JSON are rejected. Settings are ordinary
package input, not capability grants; do not store secrets. The normalized
object is included in the hook envelope as `config`, and a package may ignore
unknown keys.

The **Preview** action runs only the selected installed package with bounded
sample `home.decorate` data and submitted configuration. It uses a fresh
sandbox, does not change enabled state, configuration or history, and is shown
only to the requesting administrator. A disabled dependency blocks preview;
the selected package itself may be previewed while disabled. Output has the
same JSON and plain-text notice limits as a live hook. This is not a full home
page preview and does not run other packages.

Package installation, capability grants, health UI and version rollback are not implemented in the panel. Filesystem
package changes must be made by a trusted operator and deployed as an atomic
release. Do not expose the plugin directory as a web upload destination.

## Settings history and compatibility

The administrator-only appearance page shows the latest 20 recorded settings
changes across appearance saves, theme restores and explicit WASM enable/disable
actions. Events contain the administrator's stable user ID, UTC timestamp,
action and target theme/package ID. Draft previews, denied/invalid requests and
unchanged effective settings do not add events. Dependency suspension is a
derived status, not a separate administrator action.

At most 100 events are retained for appearance settings and 100 for plugin
preferences. Older events are dropped on the next successful change. Each
settings change and its event use a single primary database write; offline
database backups include both. Public page rendering skips decoding history
entries. This is bounded operational history, not an immutable security log;
it does not record authentication failures, package-file changes, old/new
setting values or all other administrative actions. It has no separate export
or erasure interface, and database operators can alter it.

Appearance records gain an optional `History` field. The plugin preference key
`settings:plugins-disabled` now writes a version-1 envelope with `version`,
`Disabled` and `History` fields. Existing bare disabled-ID maps are read without
losing preferences; they convert when a real preference change is saved. No
historical events are fabricated. Malformed preference maps and unknown-version
plugin records block preference changes and hook execution rather than enabling
packages.
Older application versions cannot read the envelope: rolling back the binary
requires its matching pre-upgrade database snapshot. Do not point an older
binary at an upgraded database. The one-step theme rollback does not downgrade
the schema or erase recorded history.

## Built-in modules

- Pin up to eight existing discussions by ID. The sidebar checks current forum
  visibility and omits deleted or moderated topics.
- Statistics show topic and message totals for the forums shown to the viewer,
  using the existing forum counters. No site-wide member count or peak is shown.
- Presence is off by default. It uses process-local, bounded activity from
  signed-in page views, with a 15-minute online and a 5-minute active window.
  Only signed-in viewers can see active member names. No guest or IP tracking.
- A sponsored slot accepts a plain text title and one HTTPS link, not HTML or
  scripts. Clear both fields to remove the ad.
- When external media is enabled, use `[img]https://…[/img]`,
  `[link]https://…[/link]` and `[download]https://…[/download]` in posts. Images
  load in the browser only after opening the preview; the server never fetches
  the resources. Download links point to external files; the external server
  decides whether they download or open.
- Enable local file attachments separately in `/admin/appearance`. One file
  up to 512 KiB can be attached to each visible post, with instance limits of
  256 files and 64 MiB of registered file contents. The active author can
  manage the attachment during the 15-minute edit window in an open thread
  if they still have posting permission. An administrator with forum access
  can manage attachments on visible posts, including in closed threads.
  Downloads recheck forum permissions and post visibility, are not cached,
  and are served as downloads, never inline. No malware scanning is provided.
- Attachments are stored in the database and are included in offline database
  backups; never expose the data directory as static files. Removing an
  attachment deletes it from the live database view but does not erase prior
  backups. A failed upload can leave a capacity reservation, which the
  attachment's removal action clears. Disabling attachments hides links and
  blocks downloads and uploads without deleting stored contents.
- Preview an installed theme on the settings page without changing preferences.
  Saving the default applies to users without an explicit theme preference.
- Customize the accent color, font preset, maximum site width, and corner
  radius. An empty field keeps the active theme's default. Overrides apply
  also to users who picked another installed theme, when that theme uses the
  standard CSS variables. No raw CSS or external fonts are accepted here.
- Preview a draft in the administration page before saving. Preview does not
  publish it. Use the color-scheme toggle to check the accent on both light
  and dark backgrounds, especially links. The accent fill gets an automatic
  contrasting foreground, but that does not guarantee link contrast.
- Restore the previous saved theme settings in one step. The rollback survives
  restarts and doesn't change announcements, advertising, media, or WASM
  preferences. This does not restore theme package files.
