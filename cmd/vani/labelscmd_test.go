package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// `vani labels --json` is the label vocabularies' wire form: it must equal the
// golden file the Swift app and the extension replay, and it needs no config,
// ledger or state (the app reads it once at launch).
func TestLabelsJSONMatchesGolden(t *testing.T) {
	setup(t) // no `init`: labels reads no config
	o, _ := mustCode(t, 0, "labels", "--json")
	raw, err := os.ReadFile("../../internal/jobview/testdata/labels.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal([]byte(o), &got); err != nil {
		t.Fatalf("not one JSON value: %v\n%s", err, o)
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("labels --json differs from the golden file:\n%s", o)
	}
}

func TestLabelsTextAndUsage(t *testing.T) {
	setup(t)
	o, _ := mustCode(t, 0, "labels")
	for _, w := range []string{"Strong fit", "Closed", "AI Engineer", "Sponsors visas"} {
		if !strings.Contains(o, w) {
			t.Errorf("text output lacks %q:\n%s", w, o)
		}
	}
	mustCode(t, 2, "labels", "--bogus")
	mustCode(t, 2, "labels", "extra")
}
