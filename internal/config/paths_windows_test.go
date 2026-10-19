//go:build windows

package config

import "testing"

// On Windows itself the default lands under %AppData%\Vani with Windows
// separators. Compiled by GOOS=windows go vet; Vani does not run on Windows.
func TestPathsOnWindows(t *testing.T) {
	get := func(k string) string {
		return map[string]string{"APPDATA": `C:\Users\u\AppData\Roaming`}[k]
	}
	c, s := Paths(get, `C:\Users\u`)
	if c != `C:\Users\u\AppData\Roaming\Vani\config.json` || s != `C:\Users\u\AppData\Roaming\Vani\state` {
		t.Fatalf("%s %s", c, s)
	}
}
