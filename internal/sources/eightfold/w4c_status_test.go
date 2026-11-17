package eightfold

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// w4c137: the envelope failure names the status value (or says it is missing),
// never the address of the pointer that holds it.
func TestW4cEnvelopeErrorPrintsTheStatusNotAPointer(t *testing.T) {
	for name, tc := range map[string]struct{ body, want string }{
		"status 500":      {`{"status":500,"error":{"message":"boom"},"data":null}`, "envelope status 500"},
		"status 0":        {`{"status":0,"error":{},"data":null}`, "envelope status 0"},
		"status missing":  {`{"error":{"message":"gone"},"data":null}`, "envelope has no status"},
		"status null":     {`{"status":null,"error":{},"data":null}`, "envelope has no status"},
		"status negative": {`{"status":-1,"error":{},"data":null}`, "envelope status -1"},
	} {
		s := &source{d: srctest.NewFunc(func(srctest.Request) (srctest.Reply, bool) {
			return srctest.Reply{Status: 200, Body: []byte(tc.body)}, true
		}), now: time.Now, wait: noWait}
		_, err := s.List(context.Background(), board("x"))
		if !errors.Is(err, ErrShape) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want ErrShape saying %q", name, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), "0xc") || strings.Contains(err.Error(), "0x1") {
			t.Errorf("%s: the error prints a pointer: %v", name, err)
		}
	}
}
