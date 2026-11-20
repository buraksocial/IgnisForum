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

The appearance page (`/admin/appearance`) lists installed WASM packages. It
can configure the built-in modules described below, not the WASM packages.
Package install, preview, enable/disable, per-plugin configuration,
permissions/capability grants, health UI, and hot rollback are not implemented.
Until that administration exists, package changes must be made by a trusted
operator with filesystem access and deployed as an atomic release. Do not expose
the plugin directory as a web upload destination.

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
  decides whether they download or open. This is not a file upload service.
- Preview an installed theme on the settings page without changing preferences.
  Saving the default applies to users without an explicit theme preference.
