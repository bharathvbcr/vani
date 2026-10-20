package main

import "github.com/bharathvbcr/Vani/internal/jobview"

// rowOf is the one place a jobJSON becomes the versioned row contract
// (internal/jobview) the Mac app and the browser extension read. Every list,
// show, mark and find reply gets its row from here, after enrich has set the
// score, reasons and age.
func rowOf(j jobJSON) jobview.Row {
	var reasons []jobview.Reason
	for _, r := range j.Reasons {
		reasons = append(reasons, jobview.Reason(r))
	}
	return jobview.Build(jobview.Inputs{
		Key: string(j.Key), Title: j.Title, Company: j.Company, Location: j.Location,
		Status: string(j.Status), Relevance: j.Relevance, Priority: j.Priority,
		RelevanceBasis: string(j.RelevanceBasis), RoleFit: j.Triage["role_fit"],
		Reasons:  reasons,
		DupLinks: []string{j.DupOf, j.DupCandidateOf, j.DupSiblingOf},
		AgeDays:  j.AgeDays, FreshSrc: j.FreshSrc,
	})
}
