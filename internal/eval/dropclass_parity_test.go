package eval_test

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// eval names the pipeline's exclusion and title markers without importing the
// pipeline; these pin the copies to the writers.
func TestDropClassMarkersMatchThePipeline(t *testing.T) {
	if eval.AnnExcludeMatched != pipeline.AnnExcluded {
		t.Errorf("eval.AnnExcludeMatched %q, pipeline.AnnExcluded %q", eval.AnnExcludeMatched, pipeline.AnnExcluded)
	}
	if eval.ExcludeBackend != pipeline.ExcludeBackend {
		t.Errorf("eval.ExcludeBackend %q, pipeline.ExcludeBackend %q", eval.ExcludeBackend, pipeline.ExcludeBackend)
	}
	if eval.TitleBackend != pipeline.TitleBackend {
		t.Errorf("eval.TitleBackend %q, pipeline.TitleBackend %q", eval.TitleBackend, pipeline.TitleBackend)
	}
}

// The words that mark an only_locations drop on an unrecognised place are
// written by config.Exclude.Match and read by the pipeline (the run record, the
// prefilter class) and by eval (the drop class); one change to them without the
// others would silently stop the class.
func TestUnrecognisedLocationMarkIsOneStringEverywhere(t *testing.T) {
	if eval.UnrecognisedLocationMark != config.UnrecognisedLocationMark || pipeline.LocationUnrecognisedMark != config.UnrecognisedLocationMark {
		t.Errorf("eval %q, pipeline %q, config %q", eval.UnrecognisedLocationMark, pipeline.LocationUnrecognisedMark, config.UnrecognisedLocationMark)
	}
	// The writer's reason is what the readers recognise.
	x := config.Exclude{OnlyLocations: []string{"United States"}}
	why, ok := x.Match(posting.Posting{Title: "T", Location: "Zzyzxville"})
	if !ok || !pipeline.UnrecognisedLocationReason(why) || !pipeline.UnrecognisedLocationReason("excluded: "+why) {
		t.Fatalf("pipeline does not read the config's reason %q", why)
	}
	r := ledger.Record{Key: "gh:a:1", Status: posting.Dropped, Annotations: map[string]string{
		"triage.backend": eval.ExcludeBackend, eval.AnnExcludeMatched: "excluded: " + why}}
	if got := eval.DropClass(r, nil); got != "exclude:location_unrecognised" {
		t.Errorf("DropClass = %q", got)
	}
	if got := pipeline.PrefilterRule("excluded: " + why); got != "exclude_location_unrecognised" {
		t.Errorf("PrefilterRule = %q", got)
	}
}
