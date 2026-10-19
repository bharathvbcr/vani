package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// LocalShadowPrefix starts every local shadow backend name ("local-<model>-<mode>").
// Rows recorded before the model was named in the backend are filed under the bare
// name "local".
const LocalShadowPrefix = "local-"

// BackendName is the name the local decision model answers under in routing,
// ledger annotations (triage.<slot>.shadow_backend) and the eval:
// "local-<model>-<questions>". The model is the GGUF file name without ".gguf"
// when Vani starts the server (the file is what differs between two
// quantisations of one model; Model is only the string sent to the server), else
// Model, normalised like BackendName; questions is "combined" or "per_slot". Two
// candidate models, or one model asked two ways, are then two shadow backends and
// never pool in `vani eval`. It is empty when the model leaves no name; Validate
// refuses that and any name over MaxBackendName bytes instead of truncating, since
// two truncated names would pool again.
func (l Local) BackendName() string {
	id := l.Model
	if l.Server != nil && l.Server.ModelPath != "" {
		id = filepath.Base(l.Server.ModelPath)
		if strings.EqualFold(filepath.Ext(id), ".gguf") {
			id = id[:len(id)-len(".gguf")]
		}
	}
	id = BackendName(id)
	if id == "" {
		return ""
	}
	mode := l.Questions
	if mode == "" {
		mode = "combined"
	}
	return LocalShadowPrefix + id + "-" + mode
}

// IsLocalShadow reports whether a recorded backend name is a local shadow's:
// the pre-naming "local" or "local-<model>-<mode>".
func IsLocalShadow(name string) bool {
	return name == "local" || strings.HasPrefix(name, LocalShadowPrefix)
}

func checkLocalShadowName(l Local) error {
	n := l.BackendName()
	switch {
	case n == "":
		return fmt.Errorf("leaves no shadow backend name (the model has no letters or digits)")
	case len(n) > MaxBackendName:
		return fmt.Errorf("shadow backend name %q is longer than %d bytes; shorten the GGUF file name or decide.local.model", n, MaxBackendName)
	}
	return nil
}
