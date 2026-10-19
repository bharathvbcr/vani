package daemon

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// xmlWellFormed tokenizes s and reports the first syntax error. The plist
// DOCTYPE is a directive, which the decoder passes through.
func xmlWellFormed(s string) error {
	d := xml.NewDecoder(strings.NewReader(s))
	for {
		_, err := d.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
