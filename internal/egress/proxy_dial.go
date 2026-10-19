package egress

import (
	"context"
	"net"
	"strings"
)

// dialVetted opens a TCP connection to host:port under the Client's rules. It
// is the same dial the HTTP transport uses: allowlist, DNS vetting of every
// answer, and a connection to the vetted literal IP, so the proxy cannot reach
// anything the Client itself could not. The host is normalised exactly as
// hostKey does for URLs.
func (c *Client) dialVetted(ctx context.Context, host, port string) (net.Conn, error) {
	host = strings.TrimRight(strings.ToLower(host), ".")
	return c.dial(ctx, "tcp", net.JoinHostPort(host, port))
}
