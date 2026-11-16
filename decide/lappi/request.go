package lappi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
)

// Slot types this client can read an answer for.
const (
	SlotChoice = "choice"
	SlotSpan   = "span"
)

// SlotSpec is one slot of a request.
type SlotSpec struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Options []string `json:"options,omitempty"`
}

// requestMetadata encodes as {} — the contract's `metadata: {}`.
type requestMetadata struct{}

// Request is one request line (schema-api.md "Request").
type Request struct {
	SchemaVersion int             `json:"schema_version"`
	Task          string          `json:"task"`
	ContextB64    string          `json:"context_b64"`
	ContextLen    int             `json:"context_len"`
	Question      string          `json:"question"`
	Slots         []SlotSpec      `json:"slots"`
	Route         string          `json:"route"`
	ExampleID     string          `json:"example_id"`
	Metadata      requestMetadata `json:"metadata"`
}

// NewRequest builds a request for one trained family. task, question and the
// slots are rendered into the prompt verbatim (contract §4): a renamed task,
// question, slot or option serves the model a prompt it never saw, so callers
// pass the exact trained strings. context_b64 is standard, padded base64 of
// context's bytes and context_len their count; the bytes are never re-encoded
// through a string. The example id is app:task:recordID.
func NewRequest(app, task, question string, context []byte, slots []SlotSpec, recordID string) Request {
	cp := make([]SlotSpec, len(slots))
	for i, s := range slots {
		cp[i] = SlotSpec{Name: s.Name, Type: s.Type, Options: append([]string(nil), s.Options...)}
	}
	return Request{
		SchemaVersion: 1,
		Task:          task,
		ContextB64:    base64.StdEncoding.EncodeToString(context),
		ContextLen:    len(context),
		Question:      question,
		Slots:         cp,
		Route:         "generic",
		ExampleID:     app + ":" + task + ":" + recordID,
	}
}

// Encode returns the request as one JSON line, without its newline.
func (r Request) Encode() ([]byte, error) {
	if r.ContextLen < 0 {
		return nil, errors.New("lappi: negative context_len")
	}
	for _, s := range r.Slots {
		if s.Type != SlotChoice && s.Type != SlotSpan {
			return nil, errors.New("lappi: slot " + s.Name + " has a type this client cannot read: " + s.Type)
		}
	}
	return json.Marshal(r)
}
