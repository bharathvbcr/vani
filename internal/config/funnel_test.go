package config

import (
	"math"
	"strings"
	"testing"
)

func TestFunnelDefaultsAndBounds(t *testing.T) {
	var f Funnel
	if f.DescriptionMin() != DefaultMinDescriptionRunes || f.NotesFloor() != DefaultNotesMinRelevance || f.NotesCapUSD() != DefaultNotesDailyUSD {
		t.Fatalf("defaults wrong: %d %v %v", f.DescriptionMin(), f.NotesFloor(), f.NotesCapUSD())
	}
	zero, neg, nan, big, tiny := 0.0, -1.0, math.NaN(), MaxDailyUSD+1, 0.0
	for _, tc := range []struct {
		name string
		f    Funnel
		want string
	}{
		{"negative min description", Funnel{MinDescriptionRunes: -1}, "min_description_runes"},
		{"huge min description", Funnel{MinDescriptionRunes: MaxMinDescriptionRunes + 1}, "min_description_runes"},
		{"negative floor", Funnel{NotesMinRelevance: &neg}, "notes_min_relevance"},
		{"NaN floor", Funnel{NotesMinRelevance: &nan}, "notes_min_relevance"},
		{"zero notes cap", Funnel{NotesDailyUSD: &tiny}, "notes_daily_usd"},
		{"NaN notes cap", Funnel{NotesDailyUSD: &nan}, "notes_daily_usd"},
		{"over max notes cap", Funnel{NotesDailyUSD: &big}, "notes_daily_usd"},
	} {
		var got []string
		tc.f.validate(func(format string, a ...any) { got = append(got, format) })
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
	var got []string
	Funnel{NotesMinRelevance: &zero}.validate(func(format string, a ...any) { got = append(got, format) })
	if len(got) != 0 || (Funnel{NotesMinRelevance: &zero}).NotesFloor() != 0 {
		t.Fatalf("an explicit zero floor is valid and kept: %v", got)
	}
}

func TestDefaultConfigValidatesWithFunnel(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Funnel.MinDescriptionRunes = -5
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "funnel.min_description_runes") {
		t.Fatalf("Validate did not reach the funnel: %v", err)
	}
}
