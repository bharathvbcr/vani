package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// appGroup is the Mac app's App Group (AppGroup.identifier in
// macos/Sources/VaniApplyKit/AppGroup.swift).
const appGroup = "HAUHVD8M37.dev.vbcr.vani"

// maxAppSettingsBytes bounds the app settings file read; the app writes a few
// hundred bytes.
const maxAppSettingsBytes = 64 << 10

// Where a resolved path came from (Resolved.ConfigFrom and StateFrom).
const (
	FromEnv     = "env"     // VANI_CONFIG or VANI_STATE_DIR
	FromDefault = "default" // the platform default
)

// ErrAppPairMismatch is a resolution refused because the Mac app runs vani on
// another config or state dir than the platform default this process fell
// back to: reading or writing the default would silently use a ledger the app
// never shows.
var ErrAppPairMismatch = errors.New("config: the Mac app uses another config or state dir")

// AppSettingsPath is the Mac app's settings file under home. Its vaniConfig
// and vaniStateDir (DraftSettings.swift) name the config and state dir the app
// runs vani on; empty means the platform default.
func AppSettingsPath(home string) string {
	return filepath.Join(home, "Library", "Group Containers", appGroup, "apply", "draft-settings.json")
}

// Resolved is where this process keeps its config and state, and why.
type Resolved struct {
	ConfigPath string
	StateDir   string
	// ConfigFrom and StateFrom are FromEnv or FromDefault.
	ConfigFrom string
	StateFrom  string
	// AppSettings says how the paths compare with the Mac app's settings:
	// "agrees", "absent", "not checked" (both paths from the environment, or
	// not macOS), "unreadable: <reason>", or "names <config> and <state>".
	AppSettings string
	// Warning is set when the app settings could not be read: the check was
	// skipped, which the caller must say rather than treat as a pass.
	Warning string
}

// Resolve resolves the config file and state dir (Paths), then, on macOS, when
// either came from the platform default, compares it with the Mac app's
// settings. A component the app names differently is refused with
// ErrAppPairMismatch; a component taken from the environment is never
// compared. Unreadable settings skip the check with a Warning.
func Resolve(getenv func(string) string, home, goos string) (Resolved, error) {
	c, s := pathsFor(goos, getenv, home)
	r := Resolved{ConfigPath: c, StateDir: s, ConfigFrom: FromDefault, StateFrom: FromDefault, AppSettings: "not checked"}
	if getenv("VANI_CONFIG") != "" {
		r.ConfigFrom = FromEnv
	}
	if getenv("VANI_STATE_DIR") != "" {
		r.StateFrom = FromEnv
	}
	if goos != "darwin" || (r.ConfigFrom == FromEnv && r.StateFrom == FromEnv) {
		return r, nil
	}
	app, err := readAppSettings(AppSettingsPath(home))
	switch {
	case errors.Is(err, os.ErrNotExist):
		r.AppSettings = "absent"
		return r, nil
	case err != nil:
		r.AppSettings = "unreadable: " + err.Error()
		r.Warning = "the Mac app's settings could not be read, so whether it uses this config and state dir was not checked: " + err.Error()
		return r, nil
	}
	differs := func(from, resolved, named string) bool {
		return from == FromDefault && named != "" && filepath.Clean(named) != filepath.Clean(resolved)
	}
	if differs(r.ConfigFrom, c, app.Config) || differs(r.StateFrom, s, app.StateDir) {
		wantC, wantS := orPath(app.Config, c), orPath(app.StateDir, s)
		r.AppSettings = fmt.Sprintf("names %s and %s", wantC, wantS)
		return r, fmt.Errorf("%w (%s): it runs vani on config %s and state dir %s, but this process resolved config %s and state dir %s. "+
			"Export VANI_CONFIG=%s VANI_STATE_DIR=%s, or clear the Vani config and state settings in the app",
			ErrAppPairMismatch, AppSettingsPath(home), wantC, wantS, c, s, wantC, wantS)
	}
	r.AppSettings = "agrees"
	return r, nil
}

// ResolveDefault resolves against the real environment and home directory.
func ResolveDefault() (Resolved, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Resolved{}, fmt.Errorf("config: home directory: %w", err)
	}
	return Resolve(os.Getenv, home, runtime.GOOS)
}

func orPath(named, resolved string) string {
	if named == "" {
		return resolved
	}
	return named
}

// appPair is what the app settings say about vani's paths.
type appPair struct {
	Config   string `json:"vaniConfig"`
	StateDir string `json:"vaniStateDir"`
}

func readAppSettings(path string) (appPair, error) {
	f, err := os.Open(path)
	if err != nil {
		return appPair{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxAppSettingsBytes+1))
	if err != nil {
		return appPair{}, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxAppSettingsBytes {
		return appPair{}, fmt.Errorf("%s exceeds %d bytes", path, maxAppSettingsBytes)
	}
	var p appPair
	if err := json.Unmarshal(data, &p); err != nil {
		return appPair{}, fmt.Errorf("%s is not valid JSON", path)
	}
	for _, v := range []string{p.Config, p.StateDir} {
		if v != "" && !filepath.IsAbs(v) {
			return appPair{}, fmt.Errorf("%s names a relative path", path)
		}
	}
	return p, nil
}
