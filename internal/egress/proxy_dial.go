package egress

import (
	"context"
	"fmt"
	"net"
)

// dialVetted admits one proxy tunnel and opens its TCP connection under the
// Client's rules, in the order Do applies them: the allowlist first, so a
// refused host neither waits nor takes a pacing slot (and an untrusted child
// cannot grow the per-host schedule with names it invents), then the host's
// PerHostInterval, then the transport's own dial: DNS vetting of every answer
// and a connection to the vetted literal IP, so the proxy cannot reach
// anything the Client itself could not. Pacing shares the Client's schedule,
// so tunnels and Do requests to one host are spaced together. The host is
// normalised exactly as hostKey does for URLs.
func (c *Client) dialVetted(ctx context.Context, host, port string) (net.Conn, error) {
	host = normHost(host)
	if !c.allows(host) {
		return nil, fmt.Errorf("%w: %q", ErrNotAllowed, host)
	}
	if err := c.pace(ctx, host); err != nil {
		return nil, err
	}
	return c.dial(ctx, "tcp", net.JoinHostPort(host, port))
}
