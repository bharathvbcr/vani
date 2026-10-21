package marks

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile/symlinktest"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestCompactedWriteThroughSymlinks(t *testing.T) {
	symlinktest.Run(t, func(path string) error {
		k := posting.Key("greenhouse:acme:1")
		return writeCompacted(path, map[posting.Key]Entry{k: {Key: k, Mark: Saved}})
	})
}
