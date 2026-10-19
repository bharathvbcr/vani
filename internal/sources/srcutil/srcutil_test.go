package srcutil

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

const u = "https://example.test/x"

func get(t *testing.T, r srctest.Reply) error {
	t.Helper()
	var v struct{ A int }
	return GetJSON(context.Background(), srctest.New(map[string]srctest.Reply{u: r}), u, &v)
}

func TestGetJSON(t *testing.T) {
	if err := get(t, srctest.Reply{Status: 200, Body: []byte(`{"A":1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := get(t, srctest.Reply{Status: 404}); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("404: %v", err)
	}
	for _, status := range []int{204, 301, 401, 429, 500, 503} {
		err := get(t, srctest.Reply{Status: status, Body: []byte(`{"A":1}`)})
		if err == nil || errors.Is(err, ErrBoardNotFound) {
			t.Fatalf("status %d: want a plain error, got %v", status, err)
		}
	}
	for name, body := range map[string]string{"empty": "", "truncated": `{"A":`, "trailing": `{"A":1} x`, "wrong type": `{"A":"s"}`, "html": "<html>"} {
		if err := get(t, srctest.Reply{Status: 200, Body: []byte(body)}); err == nil {
			t.Errorf("%s body: want error", name)
		}
	}
}

func TestGetJSONBodyCap(t *testing.T) {
	old := maxBody
	maxBody = 32
	t.Cleanup(func() { maxBody = old })
	ok := `{"A":1}` + strings.Repeat(" ", 25) // exactly 32 bytes
	if err := get(t, srctest.Reply{Status: 200, Body: []byte(ok)}); err != nil {
		t.Fatalf("body at the cap: %v", err)
	}
	err := get(t, srctest.Reply{Status: 200, Body: []byte(ok + " ")})
	if !errors.Is(err, ErrBodyTooLarge) {
		t.Fatalf("body over the cap: %v", err)
	}
}

type failDoer struct{}

func (failDoer) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("dial refused")
}

func TestGetJSONTransportAndNilDoer(t *testing.T) {
	var v struct{}
	if err := GetJSON(context.Background(), failDoer{}, u, &v); err == nil || !strings.Contains(err.Error(), "dial refused") {
		t.Fatalf("transport error not surfaced: %v", err)
	}
	if err := GetJSON(context.Background(), nil, u, &v); err == nil {
		t.Fatal("nil doer must error")
	}
	if err := GetJSON(context.Background(), failDoer{}, "http://[::1", &v); err == nil {
		t.Fatal("bad url must error")
	}
}

func TestCheckBoard(t *testing.T) {
	good := sources.Board{ATS: posting.Lever, Token: "acme"}
	if err := CheckBoard(good, posting.Lever); err != nil {
		t.Fatal(err)
	}
	if err := CheckBoard(good, posting.Ashby); err == nil {
		t.Error("wrong ats accepted")
	}
	for _, tok := range []string{"", "   ", "\t\n"} {
		if err := CheckBoard(sources.Board{ATS: posting.Lever, Token: tok}, posting.Lever); err == nil {
			t.Errorf("token %q accepted", tok)
		}
	}
}

func TestCheckCountAndURLAndTime(t *testing.T) {
	if CheckCount(MaxPostings) != nil {
		t.Error("cap itself must pass")
	}
	if !errors.Is(CheckCount(MaxPostings+1), ErrTooManyPostings) {
		t.Error("over the cap must fail")
	}
	for _, ok := range []string{"", "https://a.test/x", "http://a.test"} {
		if err := CheckURL("f", ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"javascript:alert(1)", "file:///etc/passwd", "data:text/html,x", "//a.test/x", "/relative", "https://", "http://[::1"} {
		if err := CheckURL("f", bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if tm, err := ParseTime("f", ""); err != nil || !tm.IsZero() {
		t.Errorf("empty time: %v %v", tm, err)
	}
	if _, err := ParseTime("f", "2026-08-03T23:05:52.543+00:00"); err != nil {
		t.Errorf("ashby format: %v", err)
	}
	if _, err := ParseTime("f", "yesterday"); err == nil {
		t.Error("garbage time accepted")
	}
}
