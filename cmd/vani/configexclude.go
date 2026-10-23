package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/bharathvbcr/Vani/internal/config"
)

// maxExcludeInput caps what `vani config exclude --set` reads from stdin:
// every list full of entries at the cap, JSON-escaped, with room to spare.
const maxExcludeInput = 256 << 10

// excludeInput is where `vani config exclude --set` reads the exclusions.
// Tests replace it.
var excludeInput io.Reader = os.Stdin

// excludeReply is the one JSON object `vani config exclude` writes.
type excludeReply struct {
	Exclude         excludeLists `json:"exclude"`
	MaxEntries      int          `json:"max_entries"`
	MaxDescriptions int          `json:"max_descriptions"`
	MaxEntryBytes   int          `json:"max_entry_bytes"`
	Error           string       `json:"error,omitempty"`
}

// excludeLists is config.Exclude with every list present, so a reader sees
// an empty list rather than a missing key.
type excludeLists struct {
	Titles        []string `json:"titles"`
	TitleUnless   []string `json:"title_unless"`
	Companies     []string `json:"companies"`
	Locations     []string `json:"locations"`
	OnlyLocations []string `json:"only_locations"`
	Descriptions  []string `json:"descriptions"`
}

func listsOf(x config.Exclude) excludeLists {
	nonNil := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	return excludeLists{Titles: nonNil(x.Titles), TitleUnless: nonNil(x.TitleUnless), Companies: nonNil(x.Companies),
		Locations: nonNil(x.Locations), OnlyLocations: nonNil(x.OnlyLocations), Descriptions: nonNil(x.Descriptions)}
}

// cmdConfigExclude prints the config's exclusions, or with --set replaces
// them with the JSON object on stdin ({"titles": [...], "locations": [...]};
// a missing list is empty, an unknown key is refused) and saves the config,
// which is validated whole first: refused exclusions leave the file
// unchanged. It is how the Mac app's Settings edits them, so the bounds and
// the checks have one owner. Saving changes no posting: the next run applies
// them, and `vani prefilter` shows what that run will do.
func (e *env) cmdConfigExclude(args []string) error {
	fs := e.newFlags("config exclude")
	set := fs.Bool("set", false, "replace the exclusions with the JSON object on stdin and save the config")
	if err := parse(fs, args); err != nil {
		return err
	}
	x, err := e.exclusions(*set)
	reply := excludeReply{Exclude: listsOf(x), MaxEntries: config.MaxExcludeEntries,
		MaxDescriptions: config.MaxExcludeDescriptions, MaxEntryBytes: config.MaxExcludeEntryLen}
	if err != nil {
		reply.Exclude, reply.Error = listsOf(config.Exclude{}), err.Error()
	}
	enc := json.NewEncoder(e.stdout)
	enc.SetEscapeHTML(false)
	if werr := enc.Encode(reply); werr != nil {
		return werr
	}
	if err != nil {
		return errAlreadyReported
	}
	return nil
}

func (e *env) exclusions(set bool) (config.Exclude, error) {
	if !set {
		cfg, err := e.loadConfig()
		if err != nil {
			return config.Exclude{}, err
		}
		return cfg.Exclude, nil
	}
	raw, err := io.ReadAll(io.LimitReader(excludeInput, maxExcludeInput+1))
	if err != nil {
		return config.Exclude{}, fmt.Errorf("config exclude: read stdin: %w", err)
	}
	if len(raw) > maxExcludeInput {
		return config.Exclude{}, fmt.Errorf("config exclude: input larger than %d bytes", maxExcludeInput)
	}
	x, err := parseExclude(raw)
	if err != nil {
		return config.Exclude{}, err
	}
	if err := e.updateConfig(func(c *config.Config) error {
		c.Exclude = x
		return nil
	}); err != nil {
		return config.Exclude{}, err
	}
	return x, nil
}

// parseExclude reads one JSON object of exclusion lists. Each entry is
// trimmed and an empty list becomes nil, so the config omits it; the config's
// own validation judges the entries.
func parseExclude(raw []byte) (config.Exclude, error) {
	var x config.Exclude
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&x); err != nil {
		return config.Exclude{}, fmt.Errorf("config exclude: stdin is not an exclusions object: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return config.Exclude{}, errors.New("config exclude: trailing data after the JSON object")
	}
	for _, l := range []*[]string{&x.Titles, &x.TitleUnless, &x.Companies, &x.Locations, &x.OnlyLocations, &x.Descriptions} {
		for i, v := range *l {
			(*l)[i] = strings.TrimSpace(v)
		}
		if len(*l) == 0 {
			*l = nil
		}
	}
	return x, nil
}
