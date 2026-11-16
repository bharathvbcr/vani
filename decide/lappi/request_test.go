package lappi

import (
	"strings"
	"testing"
)

// The code.defect_class request DevCouncil sends, used here as a known-good
// trained request: NewRequest must reproduce it byte for byte.
const (
	defectTask     = "code.defect_class"
	defectQuestion = "What kind of change is this diff, and which lines does it touch?"
)

func defectSlots() []SlotSpec {
	return []SlotSpec{
		{Name: "defect_class", Type: SlotChoice, Options: []string{"stub", "logic", "cosmetic", "clean"}},
		{Name: "defect_span", Type: SlotSpan},
	}
}

func defectRequest(context []byte, recordID string) Request {
	return NewRequest("devcouncil", defectTask, defectQuestion, context, defectSlots(), recordID)
}

const goldenGoContext = "file: src/add.go\n\n" +
	"@@ -1,3 +1,3 @@ package add\n" +
	" package add\n" +
	"-func Add(a, b int) int { return a + b }\n" +
	"+func Add(a, b int) int { return a - b }\n" +
	" "

// goldenGoRequest is the exact request line for goldenGoContext. context_b64
// was computed independently (Python base64.b64encode of the same bytes).
const goldenGoRequest = `{"schema_version":1,"task":"code.defect_class",` +
	`"context_b64":"ZmlsZTogc3JjL2FkZC5nbwoKQEAgLTEsMyArMSwzIEBAIHBhY2thZ2UgYWRkCiBwYWNrYWdlIGFkZAotZnVuYyBBZGQoYSwgYiBpbnQpIGludCB7IHJldHVybiBhICsgYiB9CitmdW5jIEFkZChhLCBiIGludCkgaW50IHsgcmV0dXJuIGEgLSBiIH0KIA==",` +
	`"context_len":142,` +
	`"question":"What kind of change is this diff, and which lines does it touch?",` +
	`"slots":[{"name":"defect_class","type":"choice","options":["stub","logic","cosmetic","clean"]},{"name":"defect_span","type":"span"}],` +
	`"route":"generic","example_id":"devcouncil:code.defect_class:c0ffee00c0ffee00c0ffee00c0ffee00","metadata":{}}`

func TestNewRequestReproducesATrainedRequestByteForByte(t *testing.T) {
	line, err := defectRequest([]byte(goldenGoContext), "c0ffee00c0ffee00c0ffee00c0ffee00").Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(line) != goldenGoRequest {
		t.Fatalf("request line:\n%s\nwant\n%s", line, goldenGoRequest)
	}
}

// The escaping is part of the bytes: & < > and U+2028/U+2029 come out escaped
// and other non-ASCII text as UTF-8, as encoding/json v1 wrote them.
func TestRequestLineEscaping(t *testing.T) {
	r := NewRequest("app", "t", "Is <it> & ok\u2028 — é?", []byte("x"),
		[]SlotSpec{{Name: "s", Type: SlotChoice, Options: []string{"a&b", "<c>"}}}, "id")
	line, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"schema_version":1,"task":"t","context_b64":"eA==","context_len":1,"question":"Is \u003cit\u003e \u0026 ok\u2028 — é?","slots":[{"name":"s","type":"choice","options":["a\u0026b","\u003cc\u003e"]}],"route":"generic","example_id":"app:t:id","metadata":{}}`
	if string(line) != want {
		t.Fatalf("request line:\n%s\nwant\n%s", line, want)
	}
}

func TestNewRequestCopiesSlots(t *testing.T) {
	slots := defectSlots()
	r := NewRequest("app", "t", "q", nil, slots, "id")
	slots[0].Options[0] = "changed"
	if r.Slots[0].Options[0] != "stub" {
		t.Fatal("request shares the caller's option slice")
	}
}

func TestEncodeRefusesUnreadableSlotTypes(t *testing.T) {
	r := NewRequest("app", "t", "q", nil, []SlotSpec{{Name: "s", Type: "score"}}, "id")
	if _, err := r.Encode(); err == nil || !strings.Contains(err.Error(), "score") {
		t.Fatalf("score slot: err = %v", err)
	}
	r = NewRequest("app", "t", "q", nil, nil, "id")
	r.ContextLen = -1
	if _, err := r.Encode(); err == nil {
		t.Fatal("negative context_len encoded")
	}
}
