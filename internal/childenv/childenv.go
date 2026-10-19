// Package childenv builds the environment Vani gives the processes it starts
// (DevPrism, rkb, Lightpanda, llama-server): only what a program needs to
// find its tools, home and temp directory. Nothing else from Vani's own
// environment, API keys above all, reaches a child.
package childenv

import (
	"os"
	"runtime"
)

// unixKeys are passed on every platform.
var unixKeys = []string{"PATH", "HOME", "TMPDIR"}

// windowsKeys are what a Windows program needs on top: without SystemRoot,
// Winsock and the crypto providers fail to load; TEMP and TMP are its temp
// directory and USERPROFILE its home.
var windowsKeys = []string{"SystemRoot", "TEMP", "TMP", "USERPROFILE"}

// Minimal returns the passed-through variables that are set, then extra
// ("KEY=value" entries the caller adds).
func Minimal(extra ...string) []string {
	return minimal(runtime.GOOS, os.LookupEnv, extra)
}

func minimal(goos string, lookup func(string) (string, bool), extra []string) []string {
	keys := unixKeys
	if goos == "windows" {
		keys = append(append([]string(nil), unixKeys...), windowsKeys...)
	}
	env := make([]string, 0, len(keys)+len(extra))
	for _, k := range keys {
		if v, ok := lookup(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return append(env, extra...)
}
