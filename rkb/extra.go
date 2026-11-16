package main

// Unknown-field preservation. bank.json is edited by hand and by other tools;
// a field rkb does not model (a note, a flag another script reads) must
// survive every rkb rewrite. Each persisted struct captures unknown keys on
// decode and writes them back, after its own fields, on encode.

import (
	"bytes"
	"encoding/json"
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
func captureExtra(data []byte, t reflect.Type, alsoKnown ...string) (map[string]json.RawMessage, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	known := knownFields(t)
	var extra map[string]json.RawMessage
	for k, v := range all {
		if known[k] || contains(alsoKnown, k) {
			continue
		}
		if extra == nil {
			extra = map[string]json.RawMessage{}
		}
		extra[k] = v
	}
	return extra, nil
}

// marshalNoEscape is json.Marshal without HTML escaping (& < > stay literal).
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// withExtra appends extra keys (sorted) to an encoded JSON object.
func withExtra(obj []byte, extra map[string]json.RawMessage) ([]byte, error) {
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
