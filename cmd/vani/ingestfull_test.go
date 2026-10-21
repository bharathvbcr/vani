package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/inbox"
)

// inbox.Write's only production caller is `jobs ingest`; a full spool must come
// out of it as a loud failure the extension host can show, never a swallowed
// error that reports "queued".
func TestIngestSurfacesAFullSpool(t *testing.T) {
	_, stateDir := setup(t)
	mustCode(t, 0, "init")
	dir := filepath.Join(stateDir, inbox.Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range inbox.MaxPending {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%020d-fill%04d.json", i+1, i)), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, stdout, stderr := ingestCLI(t, 1, ingestPayload(t, "https://careers.acme.com/search", false, ingestJobs("careers.acme.com", 2)))
	if !strings.Contains(stdout, "spool is full") {
		t.Fatalf("the JSON reply must carry the reason: stdout %q stderr %q", stdout, stderr)
	}
	if n := spooled(t, stateDir); n != inbox.MaxPending {
		t.Fatalf("pending %d, want %d: a refused ingest wrote nothing", n, inbox.MaxPending)
	}
}
