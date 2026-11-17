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
	Exclude excludeLists `json:"exclude"`
	// Revision identifies Exclude's content; --revision hands it back.
	Revision        string `json:"revision"`
	MaxEntries      int    `json:"max_entries"`
	MaxDescriptions int    `json:"max_descriptions"`
	MaxEntryBytes   int    `json:"max_entry_bytes"`
	failure
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

// cmdConfigExclude prints the config's exclusions with the revision they have
// now, or changes them and saves the config, which is validated whole first:
// refused exclusions leave the file unchanged. It is how the Mac app's
// Settings edits them, so the bounds and the checks have one owner. Saving
// changes no posting: the next run applies them, and `vani prefilter` shows
// what that run will do.
//
// Three ways to change them:
//   - --patch reads {"add": {lists}, "remove": {lists}} from stdin and applies
//     just those entries to what the config holds now, so an entry another
//     writer added in between is kept (additive).
//   - --set --revision R replaces every list with the JSON object on stdin
//     only if the exclusions still have revision R, the one a read replied
//     with; otherwise it is refused with code "stale" and nothing is written.
//   - --set alone replaces every list with no check: last writer wins, an
//     exclusion saved by anyone since the caller read them is erased. It stays
//     for callers that predate revisions; prefer the other two.
func (e *env) cmdConfigExclude(args []string) error {
	fs := e.newFlags("config exclude")
	set := fs.Bool("set", false, "replace the exclusions with the JSON object on stdin and save the config")
	revision := fs.String("revision", "", "with --set: refuse (code stale) unless the exclusions still have this revision")
	patch := fs.Bool("patch", false, `apply {"add": {lists}, "remove": {lists}} from stdin to the exclusions as they are now`)
	if err := parse(fs, args); err != nil {
		return err
	}
	switch {
	case *set && *patch:
		return usagef("--set and --patch are exclusive")
	case *revision != "" && !*set:
		return usagef("--revision goes with --set")
	}
	x, err := e.exclusions(*set, *patch, *revision)
	reply := excludeReply{Exclude: listsOf(x), Revision: x.Revision(), MaxEntries: config.MaxExcludeEntries,
		MaxDescriptions: config.MaxExcludeDescriptions, MaxEntryBytes: config.MaxExcludeEntryLen}
	if err != nil {
		reply.Exclude, reply.Revision = listsOf(config.Exclude{}), ""
	}
	return e.reply(true, &reply, err, nil)
}

func (e *env) exclusions(set, patch bool, revision string) (config.Exclude, error) {
	if !set && !patch {
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
	var change func(config.Exclude) (config.Exclude, error)
	if patch {
		add, remove, err := parseExcludePatch(raw)
		if err != nil {
			return config.Exclude{}, err
		}
		change = func(cur config.Exclude) (config.Exclude, error) { return cur.Merge(add, remove), nil }
	} else {
		x, err := parseExclude(raw)
		if err != nil {
			return config.Exclude{}, err
		}
		change = func(cur config.Exclude) (config.Exclude, error) {
			if revision != "" && cur.Revision() != revision {
				return config.Exclude{}, config.ErrStale
			}
			return x, nil
		}
	}
	var saved config.Exclude
	if err := e.updateConfig(func(c *config.Config) error {
		next, err := change(c.Exclude)
		if err != nil {
			return err
		}
		c.Exclude, saved = next, next
		return nil
	}); err != nil {
		return config.Exclude{}, err
	}
	return saved, nil
}

// decodeExcludeInput reads exactly one JSON object from raw into v, refusing
// unknown keys and trailing data.
func decodeExcludeInput(raw []byte, v any, what string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("config exclude: stdin is not %s: %w", what, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("config exclude: trailing data after the JSON object")
	}
	return nil
}

// trimExclude trims each entry and turns an empty list into nil, so the config
// omits it; the config's own validation judges the entries.
func trimExclude(x *config.Exclude) {
	for _, l := range []*[]string{&x.Titles, &x.TitleUnless, &x.Companies, &x.Locations, &x.OnlyLocations, &x.Descriptions} {
		for i, v := range *l {
			(*l)[i] = strings.TrimSpace(v)
		}
		if len(*l) == 0 {
			*l = nil
		}
	}
}

// parseExclude reads one JSON object of exclusion lists.
func parseExclude(raw []byte) (config.Exclude, error) {
	var x config.Exclude
	if err := decodeExcludeInput(raw, &x, "an exclusions object"); err != nil {
		return config.Exclude{}, err
	}
	trimExclude(&x)
	return x, nil
}

// parseExcludePatch reads {"add": {lists}, "remove": {lists}}; either may be
// missing.
func parseExcludePatch(raw []byte) (add, remove config.Exclude, err error) {
	var p struct {
		Add    config.Exclude `json:"add"`
		Remove config.Exclude `json:"remove"`
	}
	if err := decodeExcludeInput(raw, &p, `an {"add","remove"} object`); err != nil {
		return config.Exclude{}, config.Exclude{}, err
	}
	trimExclude(&p.Add)
	trimExclude(&p.Remove)
	return p.Add, p.Remove, nil
}
