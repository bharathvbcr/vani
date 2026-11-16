package main

// Unknown-field preservation. bank.json is edited by hand and by other tools;
// a field rkb does not model (a note, a flag another script reads) must
// survive every rkb rewrite. Each persisted struct captures unknown keys on
// decode and writes them back, after its own fields, on encode.

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

var knownCache sync.Map // reflect.Type -> map[string]bool

func knownFields(t reflect.Type) map[string]bool {
	if v, ok := knownCache.Load(t); ok {
		return v.(map[string]bool)
	}
	m := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		m[name] = true
	}
	knownCache.Store(t, m)
	return m
}

// captureExtra returns the keys of a JSON object that t does not declare.
// A key that differs from a field name only in case ("Text" for "text") is an
// error: field names match exactly, so it would be carried along unread while
// the field it was meant for stays empty.
func captureExtra(data []byte, t reflect.Type, alsoKnown ...string) (map[string]jsontext.Value, error) {
	var all map[string]jsontext.Value
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	known := knownFields(t)
	var extra map[string]jsontext.Value
	for _, k := range sortedKeys(all) {
		v := all[k]
		if known[k] || contains(alsoKnown, k) {
			continue
		}
		for name := range known {
			if strings.EqualFold(k, name) {
				return nil, fmt.Errorf("field %q: the field is spelled %q (names are case-sensitive)", k, name)
			}
		}
		for _, name := range alsoKnown {
			if strings.EqualFold(k, name) {
				return nil, fmt.Errorf("field %q: the field is spelled %q (names are case-sensitive)", k, name)
			}
		}
		if extra == nil {
			extra = map[string]jsontext.Value{}
		}
		extra[k] = v
	}
	return extra, nil
}

// writeOpts is how rkb writes JSON, at every level (the struct marshalers
// below start their own Marshal, so options set only on the outer call would
// not reach them). These files live in git and are read by other tools, so
// the bytes stay what they have always been:
//   - maps sorted by key (v2 would otherwise use map order);
//   - a nil list or map written as null (v2 would otherwise write [] / {});
//   - U+2028 and U+2029 written escaped, as editors and JavaScript want;
//   - a field rkb carries without modelling it keeps its escapes as written
//     ("caf\u00e9" stays so), and a verify-log entry another tool wrote
//     survives a rewrite byte for byte.
//
// & < > are written literally: bullet text is prose, not HTML.
var writeOpts = json.JoinOptions(
	json.Deterministic(true),
	json.FormatNilSliceAsNull(true),
	json.FormatNilMapAsNull(true),
	jsontext.EscapeForJS(true),
	jsontext.PreserveRawStrings(true),
)

// marshalNoEscape is rkb's compact encoding (see writeOpts).
func marshalNoEscape(v any) ([]byte, error) {
	return json.Marshal(v, writeOpts)
}

// withExtra appends extra keys (sorted) to an encoded JSON object.
func withExtra(obj []byte, extra map[string]jsontext.Value) ([]byte, error) {
	if len(extra) == 0 {
		return obj, nil
	}
	obj = bytes.TrimSpace(obj)
	if len(obj) < 2 || obj[len(obj)-1] != '}' {
		return obj, nil
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	b.Write(obj[:len(obj)-1])
	first := bytes.Equal(bytes.TrimSpace(obj[:len(obj)-1]), []byte("{"))
	for _, k := range keys {
		if !first {
			b.WriteByte(',')
		}
		first = false
		kb, _ := marshalNoEscape(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(extra[k])
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
