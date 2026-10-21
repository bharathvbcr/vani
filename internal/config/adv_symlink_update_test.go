package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Updating through a symlinked config must keep the link, or the user's
// dotfiles-managed target silently stops being the config.
func TestAdvUpdateThroughSymlinkKeepsTheLink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	if err := Save(real, valid()); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Update(link, func(c *Config) error { c.MotivationNotes = "via link"; return nil }); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the symlink was replaced by a regular file (err %v)", err)
	}
	if got := mustLoad(t, real).MotivationNotes; got != "via link" {
		t.Errorf("the target did not receive the update: %q", got)
	}
}
