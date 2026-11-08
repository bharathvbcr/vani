package cover

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
)

// maxHeaderFile is how much of a base selection or bank file is read for the
// letterhead. A larger file is not stamped.
const maxHeaderFile = 1 << 20

// preferredHeaderKeys is the order a header object is stamped in. Other keys
// follow, sorted, so two reads of the same file draw the same lines.
var preferredHeaderKeys = []string{"name", "email", "phone", "location", "url", "website", "links", "contact_lines"}

// LetterheadLines reads the base selection's header. A named header is looked
// up in the bank file. The lines are for the PDF only. A missing, unreadable,
// or oversized file returns no lines: the letter is still drawn, without a
// letterhead. The error is nil in every case, and nothing here is logged.
func LetterheadLines(basePath, bankPath string) []string {
	if strings.TrimSpace(basePath) == "" {
		return nil
	}
	raw, ok := readRegular(basePath)
	if !ok {
		return nil
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) != nil {
		return nil
	}
	header, ok := top["header"]
	if !ok {
		return nil
	}
	var name string
	if json.Unmarshal(header, &name) == nil {
		name = strings.TrimSpace(name)
		if name == "" || strings.TrimSpace(bankPath) == "" {
			return nil
		}
		bank, bok := readRegular(bankPath)
		if !bok {
			return nil
		}
		var headers struct {
			Headers map[string]json.RawMessage `json:"headers"`
		}
		if json.Unmarshal(bank, &headers) != nil {
			return nil
		}
		header = headers.Headers[name]
		if len(header) == 0 {
			return nil
		}
	}
	var v any
	if json.Unmarshal(header, &v) != nil {
		return nil
	}
	return stringsOf(v)
}

func readRegular(path string) ([]byte, bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxHeaderFile {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxHeaderFile+1))
	if err != nil || len(b) > maxHeaderFile {
		return nil, false
	}
	return b, true
}

func stringsOf(v any) []string {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return nil
		}
		return []string{s}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, stringsOf(e)...)
		}
		return out
	case map[string]any:
		seen := map[string]bool{}
		var ordered []string
		for _, k := range preferredHeaderKeys {
			if _, ok := x[k]; ok {
				ordered = append(ordered, k)
				seen[k] = true
			}
		}
		var rest []string
		for k := range x {
			if !seen[k] {
				rest = append(rest, k)
			}
		}
		sort.Strings(rest)
		ordered = append(ordered, rest...)
		var out []string
		for _, k := range ordered {
			out = append(out, stringsOf(x[k])...)
		}
		return out
	default:
		return nil
	}
}

// CodeOf is the Code of an *Error in err's chain, or "".
func CodeOf(err error) string {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}
