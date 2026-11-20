module github.com/buraksocial/IgnisForum

go 1.26.0

require (
	github.com/buraksocial/IgnisDB-Go v0.0.0
	github.com/go-chi/chi/v5 v5.1.0
	github.com/tetratelabs/wazero v1.12.0
	golang.org/x/crypto v0.58.0
	golang.org/x/net v0.60.0
	gopkg.in/yaml.v3 v3.0.1
)

require golang.org/x/sys v0.49.0 // indirect

// IgnisDB-Go lives in a sibling checkout (see embedded-usage.md: the engine is
// an internal/ package, so it can only be imported from within the same module,
// which we achieve with a local replace directive).
replace github.com/buraksocial/IgnisDB-Go => ../IgnisDB-Go
