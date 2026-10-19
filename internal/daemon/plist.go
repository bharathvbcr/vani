package daemon

import (
	"bytes"
	"encoding/xml"
	"path/filepath"
	"strconv"
)

// RestartThrottle is the least time launchd leaves between starts of the
// daemon, in seconds (ThrottleInterval).
const RestartThrottle = 300

// PlistXML renders a launchd agent definition that starts binary with args at
// load. Every string is XML-escaped.
//
// KeepAlive restarts the daemon only after a failed exit (SuccessfulExit
// false), at most once every RestartThrottle seconds. A poisoned ledger (an
// append that could not be cut back, a failed fsync) ends the daemon with
// ErrFatal and a non-zero exit, logged to vani.err.log and the run record, and
// the new process reopens the ledger, which recovers it or refuses loudly
// again. A clean exit (SIGTERM, a stop through launchctl) returns 0 and stays
// down. The owner chose this on 2026-10-19 over KeepAlive false, which left a
// poisoned daemon down until someone noticed.
func PlistXML(label, binary string, args []string, logDir string) []byte {
	var b bytes.Buffer
	str := func(s string) {
		b.WriteString("<string>")
		_ = xml.EscapeText(&b, []byte(s))
		b.WriteString("</string>\n")
	}
	key := func(k string) { b.WriteString("\t<key>" + k + "</key>\n\t") }

	b.WriteString(xml.Header)
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\">\n<dict>\n")
	key("Label")
	str(label)
	key("ProgramArguments")
	b.WriteString("<array>\n")
	for _, a := range append([]string{binary}, args...) {
		b.WriteString("\t\t")
		str(a)
	}
	b.WriteString("\t</array>\n")
	key("RunAtLoad")
	b.WriteString("<true/>\n")
	key("KeepAlive")
	b.WriteString("<dict>\n\t\t<key>SuccessfulExit</key>\n\t\t<false/>\n\t</dict>\n")
	key("ThrottleInterval")
	b.WriteString("<integer>" + strconv.Itoa(RestartThrottle) + "</integer>\n")
	key("StandardOutPath")
	str(filepath.Join(logDir, "vani.out.log"))
	key("StandardErrorPath")
	str(filepath.Join(logDir, "vani.err.log"))
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}
