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

var fieldCache sync.Map // reflect.Type -> map[string]reflect.Type

// jsonFields maps each JSON name a struct type decodes to the field's type.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	if v, ok := fieldCache.Load(t); ok {
		return v.(map[string]reflect.Type)
	}
	m := map[string]reflect.Type{}
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
		m[name] = f.Type
	}
	fieldCache.Store(t, m)
	return m
}

func knownFields(t reflect.Type) map[string]bool {
	m := map[string]bool{}
	for name := range jsonFields(t) {
		m[name] = true
	}
	return m
}

// Field names match exactly (v1 matched them ignoring case). A key that
// differs from a field name only in case ("Text" for "text") would be ignored
// or carried along unread while the field it was meant for stays empty, so it
// is an error that names the right spelling.
func miscased(key string, names []string) error {
	for _, name := range names {
		if key != name && strings.EqualFold(key, name) {
			return fmt.Errorf("field %q: the field is spelled %q (names are case-sensitive)", key, name)
		}
	}
	return nil
}

// checkKeyCase applies miscased to every object in data that decodes into a
// struct of type t, at any depth.
func checkKeyCase(data []byte, t reflect.Type) error { return checkKeys(data, t, false) }

// checkKnownKeys is checkKeyCase that also refuses a key the struct does not
// declare, naming it. A key starting with "_" is an annotation ("_about") and
// is allowed.
func checkKnownKeys(data []byte, t reflect.Type) error { return checkKeys(data, t, true) }

func checkKeys(data []byte, t reflect.Type, strict bool) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	return walkKeyCase(v, t, "$", strict)
}

func walkKeyCase(v any, t reflect.Type, path string, strict bool) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		fields := jsonFields(t)
		names := sortedKeys(fields)
		for _, k := range sortedKeys(obj) {
			if ft, ok := fields[k]; ok {
				if err := walkKeyCase(obj[k], ft, path+"."+k, strict); err != nil {
					return err
				}
			} else if err := miscased(k, names); err != nil {
				return fmt.Errorf("%s: %v", path, err)
			} else if strict && !strings.HasPrefix(k, "_") {
				return fmt.Errorf("%s: unknown key %q (not a setting rkb reads; check the spelling)", path, k)
			}
		}
	case reflect.Slice, reflect.Array:
		arr, ok := v.([]any)
		if !ok {
			return nil
		}
		for i, e := range arr {
			if err := walkKeyCase(e, t.Elem(), fmt.Sprintf("%s[%d]", path, i), strict); err != nil {
				return err
			}
		}
	case reflect.Map:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		for _, k := range sortedKeys(obj) {
			if err := walkKeyCase(obj[k], t.Elem(), path+"."+k, strict); err != nil {
				return err
			}
		}
	}
	return nil
}

// captureExtra returns the keys of a JSON object that t does not declare.
// A miscased field name is an error (see miscased).
func captureExtra(data []byte, t reflect.Type, alsoKnown ...string) (map[string]jsontext.Value, error) {
	var all map[string]jsontext.Value
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	known := knownFields(t)
	names := append(sortedKeys(known), alsoKnown...)
	var extra map[string]jsontext.Value
	for _, k := range sortedKeys(all) {
		v := all[k]
		if known[k] || contains(alsoKnown, k) {
			continue
		}
		if err := miscased(k, names); err != nil {
			return nil, err
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
