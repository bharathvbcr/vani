package posting

import (
	"strings"
	"testing"
)

// A description the size of a long real posting (about 24 KiB). The hash is
// taken for every listed posting on every board refresh.
func BenchmarkDescriptionHash(b *testing.B) {
	para := "We build LLM inference in Go and Rust on Kubernetes,\n\twith  Python tooling, CUDA kernels and observability.  "
	p := Posting{Description: strings.Repeat(para, 220)}
	b.SetBytes(int64(len(p.Description)))
	b.ReportAllocs()
	for b.Loop() {
		_ = p.DescriptionHash()
	}
}
