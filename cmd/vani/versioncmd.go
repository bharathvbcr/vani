package main

import "fmt"

// version is the product version. scripts/release.sh stamps it at build time
// with -ldflags "-X main.version=vMAJOR.MINOR.PATCH"; an unstamped build says
// "dev" so it is never mistaken for a release.
var version = "dev"

// cmdVersion prints the product version (not the native-messaging protocol
// version the Mac host and agent report for --version).
func (e *env) cmdVersion(args []string) error {
	fs := e.newFlags("version")
	asJSON := e.jsonFlag(fs, `{"version"} on stdout`)
	if err := parsePositional(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return usagef("version takes no arguments")
	}
	return e.reply(*asJSON, struct {
		Version string `json:"version"`
	}{version}, nil, func() { fmt.Fprintf(e.stdout, "vani %s\n", version) })
}
