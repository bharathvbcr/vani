package main

import (
	"reflect"
	"strings"
	"testing"
)

// The reply carries what the extension and the app read, and no field that was
// never set (ats_coverage_pct): the set of keys is pinned.
func TestResumeReplyKeysArePinned(t *testing.T) {
	want := "pdf,name,bytes,pages,selection,reason,unverified_dropped,notes,error,code"
	var got []string
	rt := reflect.TypeOf(resumeReply{})
	for i := 0; i < rt.NumField(); i++ {
		got = append(got, strings.Split(rt.Field(i).Tag.Get("json"), ",")[0])
	}
	if strings.Join(got, ",") != want {
		t.Errorf("reply keys = %s\nwant       %s", strings.Join(got, ","), want)
	}
}
