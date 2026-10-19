// Package egress is Vani's only outbound HTTP path. Every request to the
// internet goes through a Doer built here, so the host allowlist, the
// https-only rule and the private-address block are enforced where the
// socket is opened, not where a URL is chosen.
//
// The rules mirror Manvi's manvi/fetch (allowlist, DNS-pinned dialing,
// blocked private/link-local/loopback ranges on every hop). manvi/fetch cannot
// be used directly: it only issues GET, reduces every body to prose and caps
// it for a model's context, and ATS APIs need raw JSON (and Workday needs
// POST). Converging on an exported Manvi egress policy is a tracked follow-up
// (docs/FOLLOWUPS.md); until then this package owns the rules for Vani.
package egress

import (
	"context"
	"net/http"
)

// Doer sends one request and returns its response. Implementations must
// enforce the egress rules on the initial request and every redirect hop.
// Callers own closing the response body.
type Doer interface {
	Do(ctx context.Context, req *http.Request) (*http.Response, error)
}
