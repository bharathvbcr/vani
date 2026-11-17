package lca

import (
	"strings"
	"testing"
)

func TestW4bImportRefusesAFileWithNothingCounted(t *testing.T) {
	cases := map[string]struct{ csv, want string }{
		"header only":     {hdr, "no data rows"},
		"header only bom": {"\xEF\xBB\xBF" + strings.ReplaceAll(hdr, "\n", "\r\n"), "no data rows"},
		"all denied": {hdr +
			"A1,Denied,2024-01-01,2024-01-02,H-1B,Acme Inc\n" +
			"A2,Denied,2024-01-01,2024-01-02,H-1B,Beta Inc\n", "2 rows read and none counted"},
		"mixed reasons": {hdr +
			"A1,Denied,2024-01-01,2024-01-02,H-1B,Acme Inc\n" +
			"A2,Certified,2024-01-01,2024-01-02,F-1,Beta Inc\n" +
			"A3,Certified,never,not a date,H-1B,Gamma Inc\n" +
			"A4,Certified,2024-01-01,2024-01-02,H-1B,Inc\n" +
			"ragged\n", "5 rows read and none counted"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Import(strings.NewReader(c.csv), "t.csv")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// The refusal names the skip reasons and their counts.
func TestW4bImportRefusalNamesTheReasons(t *testing.T) {
	_, err := Import(strings.NewReader(hdr+
		"A1,Denied,2024-01-01,2024-01-02,H-1B,Acme Inc\n"+
		"A2,Withdrawn,2024-01-01,2024-01-02,H-1B,Beta Inc\n"+
		"A3,Certified,2024-01-01,2024-01-02,F-1,Gamma Inc\n"), "t.csv")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{SkipStatus + ":2", SkipVisa + ":1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// One counted row is enough: the refusal is for an all-skipped file only.
func TestW4bImportKeepsAFileWithOneCountedRow(t *testing.T) {
	p, err := Import(strings.NewReader(hdr+
		"A1,Denied,2024-01-01,2024-01-02,H-1B,Acme Inc\n"+
		"A2,Certified,2024-01-01,2024-01-02,H-1B,Beta Inc\n"), "t.csv")
	if err != nil || p.Source.RowsCounted != 1 {
		t.Fatalf("p=%+v err=%v", p.Source, err)
	}
}
