package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
)

// A symlinked config file locks path+".lock" beside the link, not beside the
// target, so two writers using different spellings do not exclude each other.
func TestAdvLockSharedWithFileSymlink(t *testing.T) {
	shortLock(t, 150*time.Millisecond)
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	if err := Save(real, valid()); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	release := holdLock(t, real)
	defer release()
	ran := false
	err := Update(link, func(*Config) error { ran = true; return nil })
	if !errors.Is(err, ErrBusy) || ran {
		t.Errorf("err %v, fn ran %v; want ErrBusy", err, ran)
	}
}

// A required host spelled with a trailing dot is allowed by egress (normHost
// trims it); Validate compares without trimming and calls it missing.
func TestAdvRequiredHostTrailingDot(t *testing.T) {
	c := valid()
	c.AllowedHosts = []string{"boards-api.greenhouse.io.", "api.eu.lever.co", "api.ashbyhq.com", "jobs.ashbyhq.com", "openrouter.ai"}
	if _, err := egress.New(egress.Config{AllowedHosts: c.AllowedHosts}); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("egress allows the host, Validate says: %v", err)
	}
}
