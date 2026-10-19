package egress

import (
	"net"
	"testing"
)

// tracked is how many connections Close would tear down now.
func (p *Proxy) tracked() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.conns)
}

// A tunnel's two halves are recorded together: released together on an open
// proxy, and on a closed one both closed and neither left in its table. (The
// halves used to be tracked one call at a time, so a Close landing between
// them left the client half recorded.)
func TestProxyTrackPairAfterClose(t *testing.T) {
	p, _, _, _ := startProxy(t, ProxyConfig{}, defaultAnswers())
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	defer a2.Close()
	defer b2.Close()
	release, ok := p.trackPair(a1, b1)
	if !ok || p.tracked() != 2 {
		t.Fatalf("open proxy: ok %v, %d tracked", ok, p.tracked())
	}
	release()
	if n := p.tracked(); n != 0 {
		t.Fatalf("%d tracked after release", n)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	c1, c2 := net.Pipe()
	d1, d2 := net.Pipe()
	defer c2.Close()
	defer d2.Close()
	if _, ok := p.trackPair(c1, d1); ok {
		t.Fatal("closed proxy tracked a tunnel")
	}
	if n := p.tracked(); n != 0 {
		t.Fatalf("%d left in the table of a closed proxy", n)
	}
	for name, c := range map[string]net.Conn{"client": c1, "upstream": d1} {
		if _, err := c.Write([]byte("x")); err == nil {
			t.Fatalf("%s half left open", name)
		}
	}
}
