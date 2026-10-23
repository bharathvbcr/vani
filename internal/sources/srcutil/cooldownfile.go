package srcutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
)

const (
	// maxCooldownFile bounds the cooldown file read. A file over it is treated
	// as corrupt: a real one is a few hundred bytes (maxThrottleHosts entries).
	maxCooldownFile = 64 << 10
	// maxCooldownAge is how long a cooldown the runs no longer refresh is kept.
	// A host that is never asked again (its board removed) must not stay on
	// file for good.
	maxCooldownAge = 30 * 24 * time.Hour
	// maxHostName bounds a host name on file (a DNS name is at most 253 bytes).
	maxHostName = 253
	cooldownVer = 1
)

type cooldownDoc struct {
	Version int                        `json:"version"`
	Hosts   map[string]json.RawMessage `json:"hosts"`
}

type cooldownRec struct {
	Runs int    `json:"runs"`
	Last string `json:"last"`
}

// LoadCooldowns reads the cooldowns the last run left in path. It never fails
// a run: a missing file is no cooldowns, and a file that cannot be used (over
// maxCooldownFile, not JSON, another version) is none too, with a warning that
// says so. An entry that is malformed, names an invalid host, is older than
// maxCooldownAge or is beyond the maxThrottleHosts most recent is left out; the
// warning counts what was dropped. A Last in the future (a clock set back) is
// read as an entry whose backoff window has just passed.
func LoadCooldowns(path string, at time.Time) (map[string]HostCooldown, string) {
	out := map[string]HostCooldown{}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return out, ""
	}
	if err != nil {
		return out, fmt.Sprintf("host cooldowns not read: %v", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxCooldownFile+1))
	if err != nil {
		return out, fmt.Sprintf("host cooldowns not read: %v", err)
	}
	if len(raw) > maxCooldownFile {
		return out, fmt.Sprintf("host cooldowns ignored: %s is over %d bytes", path, maxCooldownFile)
	}
	var doc cooldownDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Version != cooldownVer {
		return out, fmt.Sprintf("host cooldowns ignored: %s is not a version %d cooldown file", path, cooldownVer)
	}
	dropped := 0
	for h, rm := range doc.Hosts {
		var r cooldownRec
		if json.Unmarshal(rm, &r) != nil || r.Runs < 1 || !validHost(h) {
			dropped++
			continue
		}
		last, perr := time.Parse(time.RFC3339, r.Last)
		if perr != nil {
			dropped++
			continue
		}
		if last.After(at) {
			// The clock was set back: how long ago the 429 was cannot be told, so
			// the host's backoff window is read as just over and it is probed.
			last = at.Add(-probeGap(min(r.Runs, MaxCooldownRuns)))
		}
		if at.Sub(last) > maxCooldownAge {
			dropped++
			continue
		}
		out[h] = HostCooldown{Runs: min(r.Runs, MaxCooldownRuns), Last: last.UTC()}
	}
	dropped += pruneCooldowns(out)
	if dropped > 0 {
		return out, fmt.Sprintf("host cooldowns: %d unusable or surplus entries dropped from %s", dropped, path)
	}
	return out, ""
}

// validHost accepts what url.URL.Hostname returns for a request host,
// lower-cased: letters, digits, '.', '-', '_' and ':' (an IPv6 literal).
func validHost(h string) bool {
	if h == "" || len(h) > maxHostName || h != strings.ToLower(h) {
		return false
	}
	for _, r := range h {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' || r == ':') {
			return false
		}
	}
	return true
}

// pruneCooldowns keeps the maxThrottleHosts most recent entries of m and
// returns how many it removed. Ties go to the host name, so the choice is the
// same on every run.
func pruneCooldowns(m map[string]HostCooldown) int {
	if len(m) <= maxThrottleHosts {
		return 0
	}
	hosts := make([]string, 0, len(m))
	for h := range m {
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool {
		a, b := m[hosts[i]], m[hosts[j]]
		if !a.Last.Equal(b.Last) {
			return a.Last.After(b.Last)
		}
		return hosts[i] < hosts[j]
	})
	for _, h := range hosts[maxThrottleHosts:] {
		delete(m, h)
	}
	return len(hosts) - maxThrottleHosts
}

// SaveCooldowns writes m to path atomically. It writes at most the
// maxThrottleHosts most recent entries and none older than maxCooldownAge. An
// empty m is written as a file with no hosts, so a corrupt file is replaced.
func SaveCooldowns(path string, m map[string]HostCooldown, at time.Time) error {
	keep := make(map[string]HostCooldown, len(m))
	for h, c := range m {
		if validHost(h) && c.Runs >= 1 && at.Sub(c.Last) <= maxCooldownAge {
			keep[h] = HostCooldown{Runs: min(c.Runs, MaxCooldownRuns), Last: c.Last}
		}
	}
	pruneCooldowns(keep)
	doc := cooldownDoc{Version: cooldownVer, Hosts: make(map[string]json.RawMessage, len(keep))}
	for h, c := range keep {
		b, err := json.Marshal(cooldownRec{Runs: c.Runs, Last: c.Last.UTC().Format(time.RFC3339)})
		if err != nil {
			return fmt.Errorf("host cooldowns: %w", err)
		}
		doc.Hosts[h] = b
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("host cooldowns: %w", err)
	}
	if err := atomicfile.Write(path, append(raw, '\n')); err != nil {
		return fmt.Errorf("host cooldowns: %w", err)
	}
	return nil
}
