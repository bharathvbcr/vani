package daemon

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// plistNode is any plist element: its name, its text, and its children.
type plistNode struct {
	XMLName  xml.Name
	Text     string      `xml:",chardata"`
	Children []plistNode `xml:",any"`
}

// plistDict reads a <dict>'s alternating <key>/value children into a map.
func plistDict(t *testing.T, d plistNode) map[string]plistNode {
	t.Helper()
	if d.XMLName.Local != "dict" || len(d.Children)%2 != 0 {
		t.Fatalf("not a dict of key/value pairs: %s with %d children", d.XMLName.Local, len(d.Children))
	}
	out := make(map[string]plistNode, len(d.Children)/2)
	for i := 0; i < len(d.Children); i += 2 {
		k := d.Children[i]
		if k.XMLName.Local != "key" {
			t.Fatalf("child %d is <%s>, want <key>", i, k.XMLName.Local)
		}
		if _, dup := out[k.Text]; dup {
			t.Fatalf("key %q appears twice", k.Text)
		}
		out[k.Text] = d.Children[i+1]
	}
	return out
}

func topDict(t *testing.T, out []byte) map[string]plistNode {
	t.Helper()
	var doc plistNode
	if err := xml.Unmarshal(out, &doc); err != nil {
		t.Fatalf("parse plist: %v\n%s", err, out)
	}
	if doc.XMLName.Local != "plist" || len(doc.Children) != 1 {
		t.Fatalf("want one <dict> in <plist>:\n%s", out)
	}
	return plistDict(t, doc.Children[0])
}

// The owner's 2026-10-19 choice: launchd restarts the daemon after a failed
// exit (a poisoned ledger ends it with ErrFatal), at most every 5 minutes, and
// leaves a clean exit down. KeepAlive false, the old output, fails this.
func TestPlistRestartsOnlyAFailedExitAtMostEveryFiveMinutes(t *testing.T) {
	top := topDict(t, PlistXML("dev.vani.daemon", "/opt/vani", []string{"daemon"}, "/cfg/config.json", "/state"))

	ka, ok := top["KeepAlive"]
	if !ok {
		t.Fatal("no KeepAlive")
	}
	if ka.XMLName.Local != "dict" {
		t.Fatalf("KeepAlive is <%s/>, want a dict with SuccessfulExit false", ka.XMLName.Local)
	}
	cond := plistDict(t, ka)
	if len(cond) != 1 || cond["SuccessfulExit"].XMLName.Local != "false" {
		t.Errorf("KeepAlive conditions %v, want only SuccessfulExit false", cond)
	}

	th, ok := top["ThrottleInterval"]
	if !ok || th.XMLName.Local != "integer" || strings.TrimSpace(th.Text) != "300" {
		t.Errorf("ThrottleInterval %+v, want <integer>300</integer>", th)
	}
	if RestartThrottle != 300 {
		t.Errorf("RestartThrottle %d, want 300 (five minutes)", RestartThrottle)
	}

	if top["RunAtLoad"].XMLName.Local != "true" {
		t.Error("RunAtLoad is not true")
	}
	args := top["ProgramArguments"]
	if args.XMLName.Local != "array" || len(args.Children) != 2 || args.Children[0].Text != "/opt/vani" || args.Children[1].Text != "daemon" {
		t.Errorf("ProgramArguments %+v", args)
	}
}

// A launchd agent gets no shell environment, so the plist pins the config and
// state the daemon runs against: exactly VANI_CONFIG and VANI_STATE_DIR, with
// the logs under that state dir. Any other key (an API key above all) fails.
func TestPlistPinsExactlyConfigAndStateDir(t *testing.T) {
	top := topDict(t, PlistXML("dev.vani.daemon", "/opt/vani", []string{"daemon"}, "/live/Vani/.state/config.json", "/live/Vani/.state/state"))

	ev, ok := top["EnvironmentVariables"]
	if !ok {
		t.Fatal("no EnvironmentVariables")
	}
	env := plistDict(t, ev)
	if len(env) != 2 {
		t.Errorf("EnvironmentVariables has %d keys, want exactly VANI_CONFIG and VANI_STATE_DIR: %v", len(env), env)
	}
	for k, want := range map[string]string{
		"VANI_CONFIG":    "/live/Vani/.state/config.json",
		"VANI_STATE_DIR": "/live/Vani/.state/state",
	} {
		if v := env[k]; v.XMLName.Local != "string" || v.Text != want {
			t.Errorf("%s = <%s>%q, want <string>%q", k, v.XMLName.Local, v.Text, want)
		}
	}
	for k, want := range map[string]string{
		"StandardOutPath":   "/live/Vani/.state/state/logs/vani.out.log",
		"StandardErrorPath": "/live/Vani/.state/state/logs/vani.err.log",
	} {
		if got := top[k].Text; got != want {
			t.Errorf("%s = %q, want %q (logs follow the pinned state dir)", k, got, want)
		}
	}
}

// plutil is the macOS property-list checker launchd's own format is defined
// by; where it exists the generated file must pass it.
func TestPlistPassesPlutilLint(t *testing.T) {
	plutil, err := exec.LookPath("plutil")
	if err != nil {
		t.Skip("plutil not available on this system")
	}
	p := filepath.Join(t.TempDir(), "dev.vani.daemon.plist")
	if err := os.WriteFile(p, PlistXML(`dev.vani"&<x>`, "/Applications/V&V/vani", []string{"daemon"}, `/cfg/"a&b"/<c>'.json`, "/st/<d>&e"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(plutil, "-lint", p).CombinedOutput()
	if err != nil {
		t.Fatalf("plutil -lint: %v\n%s", err, out)
	}
	if !strings.HasSuffix(strings.TrimSpace(string(out)), ": OK") {
		t.Errorf("plutil -lint said %q", out)
	}
	// Apple's own parser reads the escaped paths back unchanged.
	for k, want := range map[string]string{
		"EnvironmentVariables.VANI_CONFIG":    `/cfg/"a&b"/<c>'.json`,
		"EnvironmentVariables.VANI_STATE_DIR": "/st/<d>&e",
		"StandardErrorPath":                   "/st/<d>&e/logs/vani.err.log",
	} {
		got, err := exec.Command(plutil, "-extract", k, "raw", "-o", "-", p).Output()
		if err != nil {
			t.Fatalf("plutil -extract %s: %v", k, err)
		}
		if g := strings.TrimSuffix(string(got), "\n"); g != want {
			t.Errorf("plutil reads %s as %q, want %q", k, g, want)
		}
	}
}
