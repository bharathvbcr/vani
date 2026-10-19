//go:build !darwin

package vault

import "runtime"

// platformErr says why the vault cannot work here, so a caller sees that
// rather than an exit status from a /usr/bin/security that does not exist.
func platformErr() error { return noKeychain(runtime.GOOS) }
