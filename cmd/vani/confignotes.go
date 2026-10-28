package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/draft"
)

// maxNotesInput caps what `vani config motivation --set` reads from stdin:
// room for a note at the cap padded with whitespace, nothing more.
const maxNotesInput = 4 * draft.MaxMotivationNotesBytes

// notesInput is where `vani config motivation --set` reads the note. Tests
// replace it.
var notesInput io.Reader = os.Stdin

// motivationNotesReply is the one JSON object `vani config motivation` writes.
type motivationNotesReply struct {
	Notes    string `json:"motivation_notes"`
	MaxBytes int    `json:"max_bytes"`
	failure
}

// cmdConfigMotivation prints the config's motivation_notes, or with --set
// replaces them with stdin (empty clears them) and saves the config, which
// is validated whole first: a refused note leaves the file unchanged. It is
// how the Mac app's Settings edits the note, so the cap and the checks have
// one owner.
func (e *env) cmdConfigMotivation(args []string) error {
	fs := e.newFlags("config motivation")
	set := fs.Bool("set", false, "replace the note with stdin (empty clears it) and save the config")
	if err := parse(fs, args); err != nil {
		return err
	}
	notes, err := e.motivationNotes(*set)
	reply := motivationNotesReply{Notes: notes, MaxBytes: draft.MaxMotivationNotesBytes}
	if err != nil {
		reply = motivationNotesReply{MaxBytes: draft.MaxMotivationNotesBytes}
	}
	return e.reply(true, &reply, err, nil)
}

func (e *env) motivationNotes(set bool) (string, error) {
	if !set {
		cfg, err := e.loadConfig()
		if err != nil {
			return "", err
		}
		return cfg.MotivationNotes, nil
	}
	raw, err := io.ReadAll(io.LimitReader(notesInput, maxNotesInput+1))
	if err != nil {
		return "", fmt.Errorf("config motivation: read stdin: %w", err)
	}
	if len(raw) > maxNotesInput {
		return "", fmt.Errorf("config motivation: input larger than %d bytes", maxNotesInput)
	}
	notes := strings.TrimSpace(string(raw))
	if err := e.updateConfig(func(c *config.Config) error {
		c.MotivationNotes = notes
		return nil
	}); err != nil {
		return "", err
	}
	return notes, nil
}
