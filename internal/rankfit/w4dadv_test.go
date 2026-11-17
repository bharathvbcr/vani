package rankfit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An older binary's Update loads a file whose report carries a newer field, then
// rewrites it. The rewrite must not silently drop provenance a newer binary may
// gate on: either Update refuses (as before the tolerant decode) or the field
// survives.
func TestW4dAdvUpdateKeepsUnknownReportFields(t *testing.T) {
	p := filepath.Join(t.TempDir(), "w.json")
	body := w4dFile(`"shares":`+w4dShares, `"report":{"labels":5,"contaminated":true,"weights":{"held_out_pairs":9,"newer":1}}`)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Update(context.Background(), p, func(cur *File, _ bool) error {
		cur.SpareSeniorityMismatch = []string{"senior"}
		return nil
	})
	if err != nil {
		return // refusing the rewrite is acceptable
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "contaminated") || !strings.Contains(string(b), "newer") {
		t.Errorf("Update rewrote the file and dropped the newer report fields:\n%s", b)
	}
}
