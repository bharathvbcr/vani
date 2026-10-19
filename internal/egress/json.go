package egress

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// StatusError is a non-2xx response. Callers branch on Code, e.g. treating 404
// as "the posting is gone".
type StatusError struct {
	Code   int
	Method string
	Host   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("egress: %s %s returned HTTP %d", e.Method, e.Host, e.Code)
}

// GetJSON GETs url through d and decodes the JSON body into v. A nil v checks
// the status and discards the body.
func GetJSON(ctx context.Context, d Doer, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("egress: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	return doJSON(d, req, v)
}

// PostJSON POSTs body, marshalled as JSON, to url and decodes the JSON reply
// into v. A nil v checks the status and discards the body.
func PostJSON(ctx context.Context, d Doer, url string, body, v any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("egress: encoding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("egress: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	return doJSON(d, req, v)
}

func doJSON(d Doer, req *http.Request, v any) error {
	if d == nil {
		return errors.New("egress: nil Doer")
	}
	resp, err := d.Do(req.Context(), req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Code: resp.StatusCode, Method: req.Method, Host: req.URL.Hostname()}
	}
	if v == nil {
		_, err := io.Copy(io.Discard, resp.Body)
		return err
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("egress: decoding response from %s: %w", req.URL.Hostname(), err)
	}
	// Anything after the first value is a malformed document; reading on also
	// surfaces ErrBodyTooLarge for an oversized tail.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected data after the JSON value")
		}
		return fmt.Errorf("egress: decoding response from %s: %w", req.URL.Hostname(), err)
	}
	return nil
}
