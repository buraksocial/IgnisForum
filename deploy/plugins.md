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

This is the API/runtime foundation, not a complete plugin marketplace or admin
manager. Package install, preview, enable/disable, per-plugin configuration,
permissions/capability grants, health UI, and hot rollback are not implemented.
Until that administration exists, package changes must be made by a trusted
operator with filesystem access and deployed as an atomic release. Do not expose
the plugin directory as a web upload destination.
