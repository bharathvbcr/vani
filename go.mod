module github.com/bharathvbcr/Vani

go 1.26.6

require (
	github.com/bharathvbcr/Manvi/manvi v0.0.0
	github.com/bharathvbcr/decide v0.0.0
)

// decide is the owner's shared decision-client module, checked out beside
// Vani; there is no published version.
replace github.com/bharathvbcr/decide v0.0.0 => ../../devtools/decide
