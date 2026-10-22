package eval

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pyNormalise reimplements, in one -c program, the preprocessing of
// Lappi-decision python/qd_data/minhash.py shingle() (cut at 262144 bytes with
// errors="ignore", re \s+ -> " ", strip) and prints sha256 of the result.
// "mode" decides how raw bytes become a str before that: "strict" for valid
// input, "ignore" or "replace" for invalid input.
const pyNormalise = `
import sys, json, re, base64, hashlib
WS = re.compile(r"\s+")
cases = json.load(open(sys.argv[1]))
out = []
for c in cases:
    raw0 = base64.b64decode(c["b64"])
    text = raw0.decode("utf-8", errors=c["mode"])  # how the str reaches shingle()
    raw = text.encode("utf-8")
    if len(raw) > 262144:
        raw = raw[:262144]
        text = raw.decode("utf-8", errors="ignore")
    n = WS.sub(" ", text).strip()
    out.append(hashlib.sha256(n.encode("utf-8")).hexdigest() if n else "EMPTY")
print(json.dumps(out))
`

const pyWhitespace = `
import re, json
ws = re.compile(r"\s")
a = [c for c in range(0x110000) if not (0xD800 <= c < 0xE000) and ws.match(chr(c))]
b = [c for c in range(0x110000) if not (0xD800 <= c < 0xE000) and chr(c).strip() == ""]
print(json.dumps({"re_s": a, "strip": b}))
`

func python(t *testing.T, args ...string) []byte {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	out, err := exec.Command(py, append([]string{"-I"}, args...)...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Logf("stderr: %s", ee.Stderr)
		}
		t.Fatalf("python3: %v", err)
	}
	return out
}

// Every code point where Python's re \s or str.strip() disagrees with
// NormalizeText is a parity break.
func TestPythonWhitespaceSetsMatchGoForEveryCodePoint(t *testing.T) {
	var sets struct {
		ReS   []int `json:"re_s"`
		Strip []int `json:"strip"`
	}
	if err := json.Unmarshal(python(t, "-c", pyWhitespace), &sets); err != nil {
		t.Fatal(err)
	}
	in := func(l []int) map[int]bool {
		m := map[int]bool{}
		for _, c := range l {
			m[c] = true
		}
		return m
	}
	reS, strip := in(sets.ReS), in(sets.Strip)
	bad := 0
	for c := 0; c < 0x110000; c++ {
		if c >= 0xD800 && c < 0xE000 {
			continue
		}
		r := rune(c)
		mid, _ := NormalizeText("x" + string(r) + "y") // "x y" iff r collapses
		goCollapse := mid == "x y"
		if !goCollapse && mid != "x"+string(r)+"y" {
			t.Fatalf("U+%04X: unexpected %q", c, mid)
		}
		lead, err := NormalizeText(string(r) + "z")
		goTrim := err == nil && lead == "z"
		if goCollapse != reS[c] || goTrim != strip[c] {
			bad++
			t.Errorf("U+%04X: Go collapse=%v trim=%v; Python re.\\s=%v strip=%v", c, goCollapse, goTrim, reS[c], strip[c])
		}
	}
	t.Logf("python \\s matches %d code points, strip %d; %d disagreements", len(reS), len(strip), bad)
}

type pyCase struct {
	Name string `json:"name"`
	B64  string `json:"b64"`
	Mode string `json:"mode"`
	raw  []byte
}

func pyHashes(t *testing.T, cases []pyCase) []string {
	t.Helper()
	for i := range cases {
		cases[i].B64 = base64.StdEncoding.EncodeToString(cases[i].raw)
	}
	b, _ := json.Marshal(cases)
	f := filepath.Join(t.TempDir(), "cases.json")
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(python(t, "-c", pyNormalise, f), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// Valid UTF-8, including the cap landing mid-rune, with trailing whitespace
// exposed by the cut, and with whitespace runs at the cut.
func TestPythonGoldenHashesValidText(t *testing.T) {
	pad := func(n int) string { return strings.Repeat("a", n) }
	var cases []pyCase
	add := func(name, s string) { cases = append(cases, pyCase{Name: name, raw: []byte(s), Mode: "strict"}) }
	add("plain", "Build  things\tnow\n")
	add("sep-1c-1f", "a\x1c\x1d\x1e\x1fb\u0085c d e　f")
	add("unicode-edge", "  "+string([]rune{0x200b, 0x20, 0x61, 0x20, 0xfeff, 0x20, 0x62, 0x20, 0x180e, 0x20, 0x63, 0x20}))
	for pre := 0; pre < 4; pre++ {
		add(fmt.Sprintf("cut-mid-2byte-%d", pre), pad(MaxNormalizedBytes-1-pre)+"é"+pad(10))
		add(fmt.Sprintf("cut-mid-3byte-%d", pre), pad(MaxNormalizedBytes-1-pre)+"€"+pad(10))
		add(fmt.Sprintf("cut-mid-3byte-b-%d", pre), pad(MaxNormalizedBytes-2-pre)+"€"+pad(10))
		add(fmt.Sprintf("cut-mid-4byte-%d", pre), pad(MaxNormalizedBytes-1-pre)+"😀"+pad(10))
		add(fmt.Sprintf("cut-mid-4byte-b-%d", pre), pad(MaxNormalizedBytes-3-pre)+"😀"+pad(10))
	}
	add("cut-trailing-ws", pad(MaxNormalizedBytes-3)+"   "+"tail")
	add("cut-after-ws-run", pad(MaxNormalizedBytes-1)+" 　 tail")
	add("cut-ws-multibyte", pad(MaxNormalizedBytes-1)+"　tail")
	add("exact-cap", pad(MaxNormalizedBytes))
	add("cap-plus-1", pad(MaxNormalizedBytes+1))
	add("all-ws-then-text-after-cap", strings.Repeat(" ", MaxNormalizedBytes)+"x")
	want := pyHashes(t, cases)
	for i, c := range cases {
		got, err := NormalizedTextSHA256(string(c.raw))
		if want[i] == "EMPTY" {
			if err != ErrEmptyText {
				t.Errorf("%s: python empty, Go %q %v", c.Name, got, err)
			}
			continue
		}
		if err != nil || got != want[i] {
			t.Errorf("%s: Go %s (%v), python %s", c.Name, got, err, want[i])
		}
	}
}
