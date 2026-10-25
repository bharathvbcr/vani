package main

import (
	"errors"
	"fmt"

	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/resumestore"
)

// markApplied records Applied for k together with the resume version the owner
// applied with: the job's used version (resumestore.Used), or none when it has
// none. Marking is the owner's act and is never blocked by the resume store: a
// store that cannot be read records the mark without a version and says so on
// stderr, so the missing version is not mistaken for "no resume was used".
func (e *env) markApplied(dir string, k posting.Key) (marks.Entry, error) {
	version := 0
	st, err := resumestore.Open(e.stateDir)
	if err == nil {
		var used resumestore.Meta
		switch used, err = st.Used(string(k)); {
		case err == nil:
			version = used.Version
		case errors.Is(err, resumestore.ErrNotFound):
			err = nil
		}
	}
	if err != nil {
		fmt.Fprintf(e.stderr, "jobs mark: applied, but the used resume version could not be read and is not recorded: %s\n", oneLine(err.Error(), 200))
	}
	return marks.SetApplied(dir, k, version, now())
}
