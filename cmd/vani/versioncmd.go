package main

import "fmt"

// version is the product version. scripts/release.sh stamps it at build time
// with -ldflags "-X main.version=vMAJOR.MINOR.PATCH"; an unstamped build says
// "dev" so it is never mistaken for a release.
var version = "dev"

// cmdVersion prints the product version (not the native-messaging protocol
// version the Mac host and agent report for --version).
func (e *env) cmdVersion(args []string) error {
	if len(args) != 0 {
		return usagef("version takes no arguments")
	}
	fmt.Fprintf(e.stdout, "vani %s\n", version)
	return nil
}
