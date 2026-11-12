package main

import (
	"reflect"
	"testing"

	"github.com/bharathvbcr/Vani/internal/pipeline"
)

// The run record carries every pipeline.Summary count by the same name: a
// count Summary gains and the record drops would pass unseen.
func TestReconcileCountsCarryEverySummaryField(t *testing.T) {
	var s pipeline.Summary
	sv := reflect.ValueOf(&s).Elem()
	for i := range sv.NumField() {
		sv.Field(i).SetInt(int64(i + 1)) // distinct, so a swapped field shows
	}
	got := reflect.ValueOf(reconcileCountsOf(s)).Elem()
	st := sv.Type()
	for i := range sv.NumField() {
		name := st.Field(i).Name
		f := got.FieldByName(name)
		if !f.IsValid() {
			t.Errorf("reconcileCounts has no %s", name)
			continue
		}
		if f.Int() != sv.Field(i).Int() {
			t.Errorf("%s = %d, want %d", name, f.Int(), sv.Field(i).Int())
		}
	}
	if got.NumField() != sv.NumField() {
		t.Errorf("reconcileCounts has %d fields, Summary %d", got.NumField(), sv.NumField())
	}
}
