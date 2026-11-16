// Package jsonwire holds the encoding/json/v2 options for decide's request
// bodies. Their bytes are pinned outside this module (a trained Lappi request
// line, the Decisions API body n8n-email-engine pins in config/jev_requests.json),
// so they come out exactly as encoding/json v1 wrote them.
package jsonwire

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
)

// V1Bytes: maps sorted by key, a nil list or map written as null, and & < >
// U+2028 U+2029 escaped (as \u0026, \u003c, \u003e, \u2028, \u2029).
var V1Bytes = json.JoinOptions(
	json.Deterministic(true),
	json.FormatNilSliceAsNull(true),
	json.FormatNilMapAsNull(true),
	jsontext.EscapeForHTML(true),
	jsontext.EscapeForJS(true),
)
