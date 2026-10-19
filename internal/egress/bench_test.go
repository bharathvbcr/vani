package egress

import (
	"bytes"
	"io"
	"testing"
)

// Every response body passes through limitedBody. A 4 MiB body read through
// a 16 MiB cap, as io.ReadAll reads it.
func BenchmarkLimitedBody(b *testing.B) {
	body := bytes.Repeat([]byte("x"), 4<<20)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		lb := &limitedBody{rc: io.NopCloser(bytes.NewReader(body)), remaining: 16 << 20}
		n, err := io.Copy(io.Discard, lb)
		if err != nil || n != int64(len(body)) {
			b.Fatalf("read %d, %v", n, err)
		}
	}
}
