package main

import (
	"path/filepath"

	"github.com/bharathvbcr/Vani/internal/doctor"
)

// appendDoctorSilver adds a run's drop verdicts to the doctor's silver log
// (<state>/silver_doctor.jsonl), once per (run, posting). A verdict on a
// posting drawn from a stratum the auditor leaves out (stratumLeftOut: work
// authorization, work mode, owner exclusions) is withheld, as the auditor and
// the audit's overlays leave those out. No row is a label: only the
// comparison in `vani relevance` reads the log.
func (e *env) appendDoctorSilver(rep doctor.Report, cases []doctor.DropCase, runID, promptSHA string) (added, withheld int, err error) {
	rows, withheld := doctor.SilverRows(rep, cases, runID, promptSHA, func(stratum string) bool {
		_, left := stratumLeftOut(stratum)
		return !left
	})
	s, err := doctor.OpenSilverStore(filepath.Join(e.stateDir, doctor.SilverFile))
	if err != nil {
		return 0, withheld, err
	}
	added, err = s.Append(rows)
	return added, withheld, err
}
