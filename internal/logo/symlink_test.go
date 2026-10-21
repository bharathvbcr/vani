package logo

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/testutil/symlinktest"
)

func TestStateSaveThroughSymlinkedDir(t *testing.T) {
	symlinktest.RunDir(t, func(dir string) error {
		return Cache{Dir: dir}.save(state{Version: 1})
	})
}

func TestIconStoreThroughSymlinkedDir(t *testing.T) {
	symlinktest.RunDir(t, func(dir string) error {
		return Cache{Dir: dir}.store("example.com", "png", []byte("x"))
	})
}
