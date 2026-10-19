package egress

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// stubDoer returns canned responses without any network.
type stubDoer struct {
	status int
	body   io.Reader
	err    error
	got    *http.Request
	sent   string
}

func (s *stubDoer) Do(_ context.Context, req *http.Request) (*http.Response, error) {
	s.got = req
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		s.sent = string(b)
	}
	if s.err != nil {
		return nil, s.err
	}
	body := s.body
	if body == nil {
		body = strings.NewReader("")
	}
	return &http.Response{StatusCode: s.status, Body: io.NopCloser(body)}, nil
}

type job struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

func TestGetJSON(t *testing.T) {
	d := &stubDoer{status: 200, body: strings.NewReader(`{"id":"1","title":"Engineer"}`)}
	var j job
	if err := GetJSON(context.Background(), d, "https://example.com/jobs/1", &j); err != nil {
		t.Fatal(err)
	}
	if j != (job{"1", "Engineer"}) {
		t.Errorf("decoded %+v", j)
	}
	if d.got.Method != http.MethodGet || d.got.Header.Get("Accept") != "application/json" {
		t.Errorf("request = %s %v", d.got.Method, d.got.Header)
	}
}

func TestPostJSON(t *testing.T) {
	d := &stubDoer{status: 201, body: strings.NewReader(`{"id":"9"}`)}
	var j job
	err := PostJSON(context.Background(), d, "https://example.com/search", map[string]string{"q": "go"}, &j)
	if err != nil {
		t.Fatal(err)
	}
	if j.ID != "9" {
		t.Errorf("decoded %+v", j)
	}
	if d.got.Method != http.MethodPost || d.got.Header.Get("Content-Type") != "application/json" ||
		d.got.Header.Get("Accept") != "application/json" {
		t.Errorf("headers = %v", d.got.Header)
	}
	if d.sent != `{"q":"go"}` {
		t.Errorf("sent %q", d.sent)
	}
}

func TestPostJSONUnencodableBody(t *testing.T) {
	d := &stubDoer{status: 200}
	if err := PostJSON(context.Background(), d, "https://example.com/", make(chan int), nil); err == nil {
		t.Error("unencodable body must error")
	}
	if d.got != nil {
		t.Error("nothing should be sent")
	}
}

func TestStatusError(t *testing.T) {
	for _, code := range []int{301, 400, 404, 429, 500, 503} {
		d := &stubDoer{status: code, body: strings.NewReader(`{"id":"1"}`)}
		var j job
		err := GetJSON(context.Background(), d, "https://example.com/x?token=secret", &j)
		var se *StatusError
		if !errors.As(err, &se) || se.Code != code {
			t.Fatalf("%d: err = %v", code, err)
		}
		if j.ID != "" {
			t.Errorf("%d: body decoded despite error status", code)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Errorf("error leaks the query string: %v", err)
		}
	}
	d := &stubDoer{status: 404}
	err := PostJSON(context.Background(), d, "https://example.com/x", 1, nil)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 404 {
		t.Errorf("post 404: %v", err)
	}
}

func TestMalformedJSON(t *testing.T) {
	for name, body := range map[string]string{
		"empty":          ``,
		"truncated":      `{"id":`,
		"not json":       `<html>`,
		"trailing value": `{"id":"1"} {"id":"2"}`,
		"trailing junk":  `{"id":"1"} garbage`,
		"wrong type":     `{"id":5}`,
	} {
		d := &stubDoer{status: 200, body: strings.NewReader(body)}
		var j job
		if err := GetJSON(context.Background(), d, "https://example.com/", &j); err == nil {
			t.Errorf("%s: accepted %q", name, body)
		}
	}
	// Trailing whitespace is fine.
	d := &stubDoer{status: 200, body: strings.NewReader("{\"id\":\"1\"}\n\n")}
	var j job
	if err := GetJSON(context.Background(), d, "https://example.com/", &j); err != nil {
		t.Errorf("trailing whitespace rejected: %v", err)
	}
}

func TestNilTargetDiscardsBody(t *testing.T) {
	d := &stubDoer{status: 200, body: strings.NewReader(`not json at all`)}
	if err := GetJSON(context.Background(), d, "https://example.com/", nil); err != nil {
		t.Errorf("nil v should skip decoding: %v", err)
	}
}

func TestDoerErrorPropagates(t *testing.T) {
	d := &stubDoer{err: ErrNotAllowed}
	if err := GetJSON(context.Background(), d, "https://example.com/", &job{}); !errors.Is(err, ErrNotAllowed) {
		t.Errorf("err = %v", err)
	}
	if err := GetJSON(context.Background(), nil, "https://example.com/", &job{}); err == nil {
		t.Error("nil Doer must error")
	}
	if err := GetJSON(context.Background(), d, "://bad", &job{}); err == nil {
		t.Error("bad URL must error")
	}
}

func TestOversizedJSONBodyReportsTooLarge(t *testing.T) {
	big := `{"title":"` + strings.Repeat("a", 500) + `"}`
	body := &limitedBody{rc: io.NopCloser(strings.NewReader(big)), remaining: 50}
	d := &stubDoer{status: 200, body: body}
	var j job
	if err := GetJSON(context.Background(), d, "https://example.com/", &j); !errors.Is(err, ErrBodyTooLarge) {
		t.Errorf("err = %v, want ErrBodyTooLarge", err)
	}
	// Oversized trailing data after a complete value is also reported.
	tail := `{"id":"1"}` + strings.Repeat(" ", 200)
	d = &stubDoer{status: 200, body: &limitedBody{rc: io.NopCloser(strings.NewReader(tail)), remaining: 20}}
	if err := GetJSON(context.Background(), d, "https://example.com/", &j); !errors.Is(err, ErrBodyTooLarge) {
		t.Errorf("tail err = %v, want ErrBodyTooLarge", err)
	}
}
