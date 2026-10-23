package srcutil

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// jsonPayload is a {"items":[...]} document of at least size bytes.
func jsonPayload(size int) []byte {
	var b bytes.Buffer
	b.WriteString(`{"items":[`)
	for i := 0; b.Len() < size-64; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"id":` + strconv.Itoa(i) + `,"title":"Inference Engineer","body":"` + string(bytes.Repeat([]byte("x"), 900)) + `"}`)
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

type benchItems struct {
	Items []struct {
		ID    int    `json:"id"`
		Title string `json:"title"`
		Body  string `json:"body"`
	} `json:"items"`
}

// BenchmarkGetJSON20MiB is GetJSON through an in-process fake egress.Doer (no
// network) on a synthetic 20 MiB payload, decoded into a typed struct. MaxBody
// is 64 MiB, so 20 MiB is under the cap; the benchmark first checks that, and
// that a body past the cap is refused with ErrBodyTooLarge.
//
//	go -C <repo> test -run '^$' -bench BenchmarkGetJSON20MiB -benchmem -count=5 ./internal/sources/srcutil/
func BenchmarkGetJSON20MiB(b *testing.B) {
	const size = 20 << 20
	if MaxBody <= size {
		b.Fatalf("MaxBody %d is not above 20 MiB; benchmark at the cap instead", MaxBody)
	}
	body := jsonPayload(size)
	d := srctest.New(map[string]srctest.Reply{u: {Status: 200, Body: body}})
	var over benchItems
	big := srctest.New(map[string]srctest.Reply{u: {Status: 200, Body: bytes.Repeat([]byte(" "), MaxBody+1)}})
	if err := GetJSON(context.Background(), big, u, &over); !errors.Is(err, ErrBodyTooLarge) {
		b.Fatalf("body over MaxBody: %v, want ErrBodyTooLarge", err)
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		var v benchItems
		if err := GetJSON(context.Background(), d, u, &v); err != nil || len(v.Items) == 0 {
			b.Fatalf("GetJSON: %v (%d items)", err, len(v.Items))
		}
	}
}
