module github.com/buraksocial/IgnisForum

go 1.22

require (
	github.com/buraksocial/IgnisDB-Go v0.0.0
	github.com/go-chi/chi/v5 v5.1.0
	gopkg.in/yaml.v3 v3.0.1
)

// IgnisDB-Go lives in a sibling checkout (see embedded-usage.md: the engine is
// an internal/ package, so it can only be imported from within the same module,
// which we achieve with a local replace directive).
replace github.com/buraksocial/IgnisDB-Go => ../IgnisDB-Go
