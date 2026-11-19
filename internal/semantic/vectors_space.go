package semantic

import (
	"context"
	"fmt"
	"path/filepath"
)

// checkSpace refuses a rewrite whose new log the disk cannot hold. The new file
// is written beside the old one, which stays until the rename, so the whole new
// log (header plus every record kept, an upper bound: shed records shrink it) must
// fit in the free space. A free-space query that fails or is unsupported is not a
// pass: it is recorded in s.spaceNote for the caller to report, and the rewrite
// goes on (it still cleans up after a failed write).
func (s *chunkStore) checkSpace(ctx context.Context, keep []vecKeep, hdrLen int64) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: compaction cancelled: %w", ErrCache, err)
	}
	needed := hdrLen
	for _, k := range keep {
		needed += k.loc.size()
	}
	free := s.freeFn
	if free == nil {
		free = freeBytes
	}
	avail, err := free(filepath.Dir(s.path))
	if err != nil {
		s.spaceNote = err.Error()
		return nil
	}
	if uint64(needed) > avail {
		return fmt.Errorf("%w: needs %d bytes, %d free", ErrVectorsNoSpace, needed, avail)
	}
	return nil
}
