package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/pipeline"
)

// The run record carries every pipeline.Summary count by the same name: a
// count Summary gains and the record drops would pass unseen.
func TestReconcileCountsCarryEverySummaryField(t *testing.T) {
	var s pipeline.Summary
	sv := reflect.ValueOf(&s).Elem()
	clock := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	for i := range sv.NumField() {
		// Distinct, so a swapped field shows. The liveness rule added a bool
		// (Mass, ClockBehind) and a time (ClockAhead) beside the counts.
		name := sv.Type().Field(i).Name
		switch f := sv.Field(i); f.Kind() {
		case reflect.Int:
			f.SetInt(int64(i + 1))
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Struct:
			if f.Type() != reflect.TypeFor[time.Time]() {
				t.Fatalf("Summary.%s is a %s: teach this test how to fill it", name, f.Type())
			}
			f.Set(reflect.ValueOf(clock.Add(time.Duration(i) * time.Hour)))
		default:
			t.Fatalf("Summary.%s is a %s: teach this test how to fill it", name, f.Kind())
		}
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
		if !reflect.DeepEqual(f.Interface(), sv.Field(i).Interface()) {
			t.Errorf("%s = %v, want %v", name, f.Interface(), sv.Field(i).Interface())
		}
	}
	if got.NumField() != sv.NumField() {
		t.Errorf("reconcileCounts has %d fields, Summary %d", got.NumField(), sv.NumField())
	}
}
