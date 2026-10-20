module github.com/bharathvbcr/Vani

go 1.26.6

require (
	github.com/bharathvbcr/Manvi/manvi v0.0.0
	github.com/bharathvbcr/decide v0.0.0
	github.com/bharathvbcr/tessl/go v0.0.0
)

// decide is the owner's shared decision-client module, checked out beside
// Vani; there is no published version.
replace github.com/bharathvbcr/decide v0.0.0 => ../../devtools/decide

// tessl/go is the owner's EmbeddingGemma 2 binding (Metal, in-process via
// gusset), checked out under research/. Only the `tessl` build tag imports it
// (internal/semantic/tessl.go); the default build stays pure Go.
replace github.com/bharathvbcr/tessl/go v0.0.0 => ../../research/tessl/go
