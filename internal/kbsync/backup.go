package kbsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/childenv"
)

// Backup of DevPrism's career.db before a replace run deletes from it.
//
// DevPrism does not back career.db up before a mutating call: no source file in
// the DevPrism checkout writes career-db-backups/ (searched with
// `rg -uu "career-db-backups"` over the repository, no hit outside Vani's
// notes), and career_delete_block deletes the row and its embeddings with
// nothing kept (tools_career.rs:1044-1060, career_db/mod.rs delete_block_blocking).
// So Vani takes the copy, with SQLite's own online backup (`sqlite3 .backup`),
// which reads a consistent snapshot of a WAL database whether or not another
// process, the desktop app included, is writing, and folds the -wal in. A plain
// file copy of career.db, -wal and -shm taken while a writer commits can tear;
// that is the failure this avoids. The copy is then read back: SQLite's
// quick_check must say ok and the block ids it holds are returned so the caller
// can check them against what DevPrism lists.

// ErrBackup wraps every backup problem.
var ErrBackup = errors.New("kbsync: career.db backup")

// Backup bounds.
const (
	// BackupTimeout bounds each sqlite3 invocation.
	BackupTimeout = 2 * time.Minute
	// KeepBackups is how many dated backups the backup directory keeps.
	KeepBackups = 10
	// maxBackupOutput caps what a verification read may print.
	maxBackupOutput = 1 << 20
)

var backupNameRe = regexp.MustCompile(`^career-\d{8}-\d{6}\.db$`)

// sqliteCandidates are where the sqlite3 CLI is looked for; macOS ships one.
var sqliteCandidates = []string{"/usr/bin/sqlite3", "/opt/homebrew/bin/sqlite3", "/usr/local/bin/sqlite3"}

// BackupInfo describes a verified backup.
type BackupInfo struct {
	Path string
	// BlockIDs are the ids of the blocks the backup holds, sorted.
	BlockIDs []string
	Bytes    int64
	// Pruned counts older backups removed to keep KeepBackups; PruneErr is the
	// first failure doing so (the backup itself is good).
	Pruned   int
	PruneErr error
}

// Backuper writes backups. SQLite is the sqlite3 binary; empty searches
// sqliteCandidates.
type Backuper struct {
	SQLite string
}

func (b Backuper) binary() (string, error) {
	if b.SQLite != "" {
		return b.SQLite, nil
	}
	for _, p := range sqliteCandidates {
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%w: no sqlite3 binary found (looked in %s); install one or take the copy by hand", ErrBackup, strings.Join(sqliteCandidates, ", "))
}

// Backup copies the database at db into dir as career-YYYYMMDD-HHMMSS.db
// (mode 0600, dir 0700), verifies it, and prunes all but the newest
// KeepBackups. It refuses a missing or empty db, a destination that exists, and
// any path SQLite's dot-command quoting could not carry.
func (b Backuper) Backup(ctx context.Context, db, dir string, now time.Time) (BackupInfo, error) {
	for name, p := range map[string]string{"career.db": db, "backup dir": dir} {
		if !filepath.IsAbs(p) {
			return BackupInfo{}, fmt.Errorf("%w: %s path %q is not absolute", ErrBackup, name, p)
		}
		if strings.ContainsAny(p, "'\"\n\r\x00") {
			return BackupInfo{}, fmt.Errorf("%w: %s path has a quote or control character", ErrBackup, name)
		}
	}
	fi, err := os.Lstat(db)
	switch {
	case err != nil:
		return BackupInfo{}, fmt.Errorf("%w: %v", ErrBackup, err)
	case !fi.Mode().IsRegular():
		return BackupInfo{}, fmt.Errorf("%w: %s is not a regular file", ErrBackup, db)
	case fi.Size() == 0:
		return BackupInfo{}, fmt.Errorf("%w: %s is empty", ErrBackup, db)
	}
	sqlite, err := b.binary()
	if err != nil {
		return BackupInfo{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return BackupInfo{}, fmt.Errorf("%w: %v", ErrBackup, err)
	}
	dest := filepath.Join(dir, "career-"+now.UTC().Format("20060102-150405")+".db")
	if _, err := os.Lstat(dest); err == nil {
		return BackupInfo{}, fmt.Errorf("%w: %s already exists", ErrBackup, dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return BackupInfo{}, fmt.Errorf("%w: %v", ErrBackup, err)
	}
	// mode=rw never creates a missing database; a bare path would.
	if _, err := runSQLite(ctx, sqlite, "-batch", "-bail", sqliteURI(db, "mode=rw"), ".backup '"+dest+"'"); err != nil {
		os.Remove(dest)
		return BackupInfo{}, err
	}
	// A copy that cannot be shown good is removed: a half-checked file named
	// like a backup is how a deletion gets run against nothing.
	if err := os.Chmod(dest, 0o600); err != nil {
		os.Remove(dest)
		return BackupInfo{}, fmt.Errorf("%w: %v", ErrBackup, err)
	}
	info, err := verifyBackup(ctx, sqlite, dest)
	if err != nil {
		os.Remove(dest)
		return BackupInfo{}, err
	}
	info.Pruned, info.PruneErr = pruneBackups(dir, KeepBackups)
	return info, nil
}

// verifyBackup reads the copy back, immutable so the read touches nothing.
func verifyBackup(ctx context.Context, sqlite, path string) (BackupInfo, error) {
	out, err := runSQLite(ctx, sqlite, "-batch", "-bail", sqliteURI(path, "immutable=1"),
		"PRAGMA quick_check;", "SELECT id FROM blocks ORDER BY id;")
	if err != nil {
		return BackupInfo{}, err
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 0 || lines[0] != "ok" {
		return BackupInfo{}, fmt.Errorf("%w: quick_check of %s did not say ok: %s", ErrBackup, path, truncate(strings.Join(lines, "; "), 300))
	}
	fi, err := os.Stat(path)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("%w: %v", ErrBackup, err)
	}
	return BackupInfo{Path: path, BlockIDs: lines[1:], Bytes: fi.Size()}, nil
}

// sqliteURI is a file: URI for path with one query parameter. SQLite's URI
// parser needs '%', '?' and '#' escaped; spaces are escaped too.
func sqliteURI(path, query string) string {
	r := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23", " ", "%20")
	return "file://" + r.Replace(path) + "?" + query
}

func runSQLite(ctx context.Context, sqlite string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, BackupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, sqlite, args...)
	cmd.Env = childenv.Minimal()
	cmd.Stdin = nil
	var out, errw capWriter
	cmd.Stdout, cmd.Stderr = &out, &errw
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("%w: sqlite3: %v", ErrBackup, ctx.Err())
		}
		return "", fmt.Errorf("%w: sqlite3: %v: %s", ErrBackup, err, truncate(strings.TrimSpace(errw.buf.String()), 400))
	}
	if out.buf.Len() >= maxProcOutput {
		return "", fmt.Errorf("%w: sqlite3 printed more than %d bytes", ErrBackup, maxProcOutput)
	}
	return out.buf.String(), nil
}

// pruneBackups removes all but the newest keep files named like a backup. A
// file that does not match the pattern is never touched.
func pruneBackups(dir string, keep int) (int, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	var names []string
	for _, e := range ents {
		if e.Type().IsRegular() && backupNameRe.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	n := 0
	for ; n < len(names)-keep; n++ {
		if err := os.Remove(filepath.Join(dir, names[n])); err != nil {
			return n, err
		}
	}
	return n, nil
}

// CheckBackupMatches errors unless the backup holds exactly the block ids
// DevPrism lists now: a backup that is missing a block DevPrism has (or has one
// it lacks) is not the copy a deletion can be undone from.
func CheckBackupMatches(info BackupInfo, current DevPrismState) error {
	have := map[string]bool{}
	for _, id := range info.BlockIDs {
		have[id] = true
	}
	var missing, extra []string
	for id := range current.Blocks {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	for id := range have {
		if _, ok := current.Blocks[id]; !ok {
			extra = append(extra, id)
		}
	}
	if len(missing)+len(extra) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return fmt.Errorf("%w: %s holds %d blocks and DevPrism lists %d now (%d in DevPrism but not in the backup, %d the other way); career.db changed while the run started, nothing was changed, run it again",
		ErrBackup, info.Path, len(info.BlockIDs), len(current.Blocks), len(missing), len(extra))
}
