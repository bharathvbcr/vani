package decide

import (
	"errors"
	"fmt"
)

// Size bounds a Question must respect so it works on every backend.
const (
	MaxContextBytes = 64 << 10
	MaxOptionBytes  = 512
	MaxPromptBytes  = 4096
	MaxTaskBytes    = 256
)

// ErrInvalidQuestion is wrapped by every ValidateQuestion failure.
var ErrInvalidQuestion = errors.New("decide: invalid question")

// ErrInvalidValue is returned by CheckValue for a value outside a slot's options.
var ErrInvalidValue = errors.New("decide: value is not one of the slot's options")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidQuestion, fmt.Sprintf(format, args...))
}

// ValidateQuestion reports why q cannot be asked of any backend. It never
// echoes Context or Prompt text, only structure.
func ValidateQuestion(q Question) error {
	switch {
	case q.Task == "":
		return invalid("task is empty")
	case len(q.Task) > MaxTaskBytes:
		return invalid("task is %d bytes, max %d", len(q.Task), MaxTaskBytes)
	case q.Prompt == "":
		return invalid("prompt is empty")
	case len(q.Prompt) > MaxPromptBytes:
		return invalid("prompt is %d bytes, max %d", len(q.Prompt), MaxPromptBytes)
	case len(q.Context) > MaxContextBytes:
		return invalid("context is %d bytes, max %d", len(q.Context), MaxContextBytes)
	case len(q.Slots) == 0:
		return invalid("no slots")
	}
	names := make(map[string]bool, len(q.Slots))
	for i, s := range q.Slots {
		if s.Name == "" {
			return invalid("slot %d has no name", i)
		}
		if names[s.Name] {
			return invalid("duplicate slot name %q", s.Name)
		}
		names[s.Name] = true
		if n := len(s.Options); n < 2 || n > MaxOptions {
			return invalid("slot %q has %d options, want 2..%d", s.Name, n, MaxOptions)
		}
		seen := make(map[string]bool, len(s.Options))
		for _, o := range s.Options {
			switch {
			case o == "":
				return invalid("slot %q has an empty option", s.Name)
			case len(o) > MaxOptionBytes:
				return invalid("slot %q has an option of %d bytes, max %d", s.Name, len(o), MaxOptionBytes)
			case seen[o]:
				return invalid("slot %q repeats option %q", s.Name, o)
			}
			seen[o] = true
		}
	}
	return nil
}

func checkValue(s Slot, value string) error {
	for _, o := range s.Options {
		if o == value {
			return nil
		}
	}
	return fmt.Errorf("%w: slot %q", ErrInvalidValue, s.Name)
}

// CheckValue is Rule 7 for backends and the router: value must be exactly one
// of the slot's options (no case folding, no trimming). Empty is rejected here;
// only a non-Answered reading may carry an empty Value.
func CheckValue(s Slot, value string) error { return checkValue(s, value) }
