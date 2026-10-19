//go:build darwin

package vault

// platformErr is nil on macOS, where security(1) and the Keychain exist.
func platformErr() error { return nil }
