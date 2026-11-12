package eval_test

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/pipeline"
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
