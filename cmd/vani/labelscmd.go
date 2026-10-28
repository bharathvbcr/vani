package main

import (
	"fmt"
	"sort"

	"github.com/bharathvbcr/Vani/internal/jobview"
)

// cmdLabels prints the label vocabularies (internal/jobview.Vocabulary): fit,
// status, category and triage options, each as value and display string in
// display order. It reads no config, ledger or state, so a surface can ask
// once at launch and never compute its own words.
func (e *env) cmdLabels(args []string) error {
	fs := e.newFlags("labels")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout")
	if err := parse(fs, args); err != nil {
		return err
	}
	v := jobview.Vocabulary()
	if *asJSON {
		return writeJSON(e.stdout, v)
	}
	section := func(name string, es []jobview.LabelEntry) {
		fmt.Fprintf(e.stdout, "%s:\n", name)
		for _, l := range es {
			fmt.Fprintf(e.stdout, "  %-36s %s\n", l.Value, l.Label)
		}
	}
	section("fit", v.Fit)
	section("status", v.Status)
	section("category", v.Category)
	slots := make([]string, 0, len(v.Triage))
	for s := range v.Triage {
		slots = append(slots, s)
	}
	sort.Strings(slots)
	for _, s := range slots {
		section(s, v.Triage[s])
	}
	return nil
}
