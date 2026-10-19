package daemon

import (
	"bytes"
	"encoding/xml"
	"path/filepath"
)

// PlistXML renders a launchd agent definition that starts binary with args at
// load. KeepAlive is false: the daemon exiting is a fault to look at, not to
// hide behind a restart loop. Every string is XML-escaped.
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
	b.WriteString("<false/>\n")
	key("StandardOutPath")
	str(filepath.Join(logDir, "vani.out.log"))
	key("StandardErrorPath")
	str(filepath.Join(logDir, "vani.err.log"))
	b.WriteString("</dict>\n</plist>\n")
	return b.Bytes()
}
