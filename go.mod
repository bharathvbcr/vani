module github.com/bharathvbcr/Vani

go 1.27.4

require (
	github.com/bharathvbcr/Manvi/manvi v0.0.0
	github.com/bharathvbcr/decide v0.0.0
	github.com/bharathvbcr/tessl/go v0.0.0
)

// decide is the shared decision-client module (question, readings, router,
// the OpenRouter Decisions API client and the Lappi client). It lives in this
// repository as a nested module (./decide) so a fresh clone builds without a
// private checkout; it keeps its own module path so it can be split out again.
replace github.com/bharathvbcr/decide v0.0.0 => ./decide

// tessl/go is the owner's EmbeddingGemma 2 binding (Metal, in-process via
// gusset), checked out under research/. Only the `tessl` build tag imports it
// (internal/semantic/tessl.go); the default build stays pure Go.
replace github.com/bharathvbcr/tessl/go v0.0.0 => ../../research/tessl/go
