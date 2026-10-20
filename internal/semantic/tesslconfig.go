package semantic

import (
	"fmt"
	"path/filepath"
	"time"
)

// TesslConfig locates the EmbeddingGemma 2 snapshot and bounds its calls.
type TesslConfig struct {
	// Snapshot is the absolute path of a Hugging Face snapshot directory of
	// google/embeddinggemma-2.
	Snapshot string
	// LoadTimeout bounds loading the model; 0 means DefaultTesslLoadTimeout.
	LoadTimeout time.Duration
	// CallTimeout bounds one Embed call; 0 means DefaultTesslCallTimeout.
	CallTimeout time.Duration
}

const (
	DefaultTesslLoadTimeout = 2 * time.Minute
	DefaultTesslCallTimeout = 60 * time.Second
	maxTesslTimeout         = 10 * time.Minute
)

func (c TesslConfig) check() error {
	switch {
	case c.Snapshot == "":
		return fmt.Errorf("%w: no embedding snapshot configured", ErrUnavailable)
	case !filepath.IsAbs(c.Snapshot):
		return fmt.Errorf("semantic: tessl snapshot %q is not an absolute path", c.Snapshot)
	case c.LoadTimeout < 0 || c.LoadTimeout > maxTesslTimeout:
		return fmt.Errorf("semantic: tessl load timeout %v is outside 0..%v", c.LoadTimeout, maxTesslTimeout)
	case c.CallTimeout < 0 || c.CallTimeout > maxTesslTimeout:
		return fmt.Errorf("semantic: tessl call timeout %v is outside 0..%v", c.CallTimeout, maxTesslTimeout)
	}
	return nil
}

func (c TesslConfig) loadTimeout() time.Duration {
	if c.LoadTimeout == 0 {
		return DefaultTesslLoadTimeout
	}
	return c.LoadTimeout
}

func (c TesslConfig) callTimeout() time.Duration {
	if c.CallTimeout == 0 {
		return DefaultTesslCallTimeout
	}
	return c.CallTimeout
}
