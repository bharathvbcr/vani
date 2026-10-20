package semantic

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTesslConfigRefusals(t *testing.T) {
	ctx := context.Background()
	if _, err := OpenTessl(ctx, TesslConfig{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("no snapshot: %v, want ErrUnavailable (semantic matching not run)", err)
	}
	for name, c := range map[string]TesslConfig{
		"relative":      {Snapshot: "models/embeddinggemma-2"},
		"negative load": {Snapshot: "/x", LoadTimeout: -time.Second},
		"huge call":     {Snapshot: "/x", CallTimeout: time.Hour},
	} {
		_, err := OpenTessl(ctx, c)
		if err == nil || errors.Is(err, ErrUnavailable) {
			t.Errorf("%s: %v, want a config error that is not mistaken for an absent model", name, err)
		}
	}
}

func TestTesslStubIsUnavailable(t *testing.T) {
	if TesslBuilt {
		t.Skip("built with -tags tessl")
	}
	_, err := OpenTessl(context.Background(), TesslConfig{Snapshot: "/nonexistent/snapshot"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stub: %v, want ErrUnavailable", err)
	}
}
