package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/resume"
)

// failNthUpsert is a fakeDevPrism whose nth upsert fails as if DevPrism died.
type failNthUpsert struct {
	*fakeDevPrism
	n, seen int
}

func (f *failNthUpsert) CallTool(ctx context.Context, name string, args any) (json.RawMessage, error) {
	if name == "career_upsert_block" {
		f.seen++
		if f.seen == f.n {
			return nil, errors.New("devprism went away")
		}
	}
	return f.fakeDevPrism.CallTool(ctx, name, args)
}

func TestKBSyncErrorSaysTheSummaryIsPartial(t *testing.T) {
	bank, rkb, _ := kbApprovedSimple(t, "/opt/devprism/devprism")
	f := &failNthUpsert{fakeDevPrism: &fakeDevPrism{}, n: 2}
	old := startKB
	startKB = func(context.Context, resume.Config) (kbClient, error) { return &kbFailClient{f}, nil }
	t.Cleanup(func() { startKB = old })
	o, e := mustCode(t, 1, "kb", "sync", "--bank", bank, "--rkb", rkb)
	if !strings.Contains(o, "partial") || !strings.Contains(o, "stopped early") || !strings.Contains(o, "1 added") {
		t.Fatalf("an errored sync must label its summary partial: stdout %q", o)
	}
	if !strings.Contains(e, "devprism went away") {
		t.Fatalf("stderr %q", e)
	}
}

func TestKBSyncSuccessSummaryIsNotLabelledPartial(t *testing.T) {
	bank, rkb, _ := kbApprovedSimple(t, "/opt/devprism/devprism")
	fakeStart(t, &fakeDevPrism{})
	o, _ := mustCode(t, 0, "kb", "sync", "--bank", bank, "--rkb", rkb)
	if strings.Contains(o, "partial") || !strings.Contains(o, "2 added") {
		t.Fatalf("stdout %q", o)
	}
}

// kbFailClient adapts failNthUpsert to the kbClient fakeStart takes.
type kbFailClient struct{ *failNthUpsert }

func (c *kbFailClient) Close() error { return c.fakeDevPrism.Close() }
