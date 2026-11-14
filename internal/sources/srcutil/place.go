package srcutil

import "strings"

// A stored posting an adapter reuses without a detail request carries the
// location it had when it was last read. A listing row that names the place
// the posting is in now shows that place moved, and a reused copy would hide
// the move until the posting's re-read turn (RefreshDays away), so an
// exclusion that dropped the posting for its old place would not be judged
// again on its new one (pipeline.SightingReopen compares the stored location
// with the listed one). Relocate carries the row's place onto the reused copy:
// no request, so nothing to bound but the text.

// MaxRowPlace bounds a row's place text in bytes. A row's text is untrusted;
// one longer than any place is not read as one.
const MaxRowPlace = 512

// RowPlace is a listing row's place text read as the place it names:
// whitespace collapsed, and "" when the row names none or the text is too
// long to be one.
func RowPlace(s string) string {
	if len(s) > MaxRowPlace*4 { // collapse nothing over a body-sized value
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > MaxRowPlace {
		return ""
	}
	return s
}

// SamePlace reports whether two place texts name the same place: equal after
// whitespace is collapsed, ignoring case.
func SamePlace(a, b string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(a), " "), strings.Join(strings.Fields(b), " "))
}

// Relocate is the location a reused copy of a stored posting carries when its
// listing row names listed: the row's place when it names one that differs
// from stored, with moved true; otherwise stored, unchanged. A row that names
// no place says nothing about it.
func Relocate(stored, listed string) (location string, moved bool) {
	l := RowPlace(listed)
	if l == "" || SamePlace(stored, l) {
		return stored, false
	}
	return l, true
}
