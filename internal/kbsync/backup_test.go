package kbsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sqliteOrSkip finds the sqlite3 binary the backup uses. These tests drive the
// real binary; without one they are skipped, loudly, and the backup is untested
// on this machine.
func sqliteOrSkip(t *testing.T) string {
	t.Helper()
	p, err := Backuper{}.binary()
	if err != nil {
		t.Skipf("no sqlite3 binary: the backup is untested here: %v", err)
	}
	return p
}

// makeCareerDB writes a WAL-mode database shaped like DevPrism's blocks table.
func makeCareerDB(t *testing.T, sqlite, path string, ids ...string) {
	t.Helper()
	sql := "PRAGMA journal_mode=WAL; CREATE TABLE blocks (id TEXT PRIMARY KEY, kind TEXT, json TEXT NOT NULL, updated_at INTEGER);"
	for _, id := range ids {
		sql += fmt.Sprintf("INSERT INTO blocks VALUES ('%s','project','{}',1);", id)
	}
	if out, err := exec.Command(sqlite, path, sql).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
}

func TestBackupCopiesAndVerifies(t *testing.T) {
	sqlite := sqliteOrSkip(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "Application Support", "career.db") // a space, as on macOS
	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		t.Fatal(err)
	}
	makeCareerDB(t, sqlite, db, "b2", "a1", "c3")
	before, _ := os.ReadFile(db)

	info, err := Backuper{}.Backup(context.Background(), db, filepath.Join(dir, "backups"), time.Date(2026, 10, 23, 22, 30, 5, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(info.Path) != "career-20261023-223005.db" || strings.Join(info.BlockIDs, ",") != "a1,b2,c3" {
		t.Fatalf("info %+v", info)
	}
	fi, err := os.Stat(info.Path)
	if err != nil || fi.Mode().Perm() != 0o600 || fi.Size() != info.Bytes || fi.Size() == 0 {
		t.Fatalf("backup file %v %v", fi, err)
	}
	if after, _ := os.ReadFile(db); string(after) != string(before) {
		t.Fatal("the backup changed the source database")
	}
	// The same ids as DevPrism lists: accepted; one block more or fewer: refused.
	cur := DevPrismState{Blocks: map[string]json.RawMessage{"a1": nil, "b2": nil, "c3": nil}}
	if err := CheckBackupMatches(info, cur); err != nil {
		t.Fatal(err)
	}
	cur.Blocks["d4"] = nil
	if err := CheckBackupMatches(info, cur); !errors.Is(err, ErrBackup) {
		t.Fatalf("a block DevPrism has and the backup lacks: %v", err)
	}
	delete(cur.Blocks, "d4")
	delete(cur.Blocks, "a1")
	if err := CheckBackupMatches(info, cur); !errors.Is(err, ErrBackup) {
		t.Fatalf("a block the backup has and DevPrism lacks: %v", err)
	}
}

// The backup is a consistent snapshot even when the -wal holds commits the main
// file has not folded in: a plain copy of career.db alone would miss them.
func TestBackupIncludesUncheckpointedWAL(t *testing.T) {
	sqlite := sqliteOrSkip(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "career.db")
	makeCareerDB(t, sqlite, db, "a1")
	// A writer holding the database open keeps its commits in the -wal.
	cmd := exec.Command(sqlite, db)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { in.Close(); cmd.Wait() }()
	if _, err := fmt.Fprintln(in, "PRAGMA wal_autocheckpoint=0; INSERT INTO blocks VALUES ('in-wal','project','{}',2);"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if fi, err := os.Stat(db + "-wal"); err == nil && fi.Size() > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Skip("could not leave commits in the -wal on this sqlite3; the case is untested here")
		}
		time.Sleep(20 * time.Millisecond)
	}
	info, err := Backuper{}.Backup(context.Background(), db, filepath.Join(dir, "backups"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(info.BlockIDs, ",") != "a1,in-wal" {
		t.Fatalf("backup holds %v, want the WAL commit too", info.BlockIDs)
	}
}

func TestBackupRefusals(t *testing.T) {
	sqlite := sqliteOrSkip(t)
	dir := t.TempDir()
	backups := filepath.Join(dir, "backups")
	at := time.Date(2026, 10, 23, 22, 30, 5, 0, time.UTC)
	ctx := context.Background()
	good := filepath.Join(dir, "career.db")
	makeCareerDB(t, sqlite, good, "a1")

	empty := filepath.Join(dir, "empty.db")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	garbage := filepath.Join(dir, "garbage.db")
	if err := os.WriteFile(garbage, []byte(strings.Repeat("this is not sqlite ", 500)), 0o600); err != nil {
		t.Fatal(err)
	}
	noBlocks := filepath.Join(dir, "noblocks.db")
	if out, err := exec.Command(sqlite, noBlocks, "CREATE TABLE other (x);").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(good, link); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.db")

	for name, db := range map[string]string{
		"missing":   missing,
		"empty":     empty,
		"garbage":   garbage,
		"no blocks": noBlocks,
		"symlink":   link,
		"relative":  "career.db",
		"quote":     filepath.Join(dir, "it's.db"),
	} {
		if _, err := (Backuper{}).Backup(ctx, db, backups, at); !errors.Is(err, ErrBackup) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("a backup of a missing database created it")
	}
	if ents, _ := os.ReadDir(backups); len(ents) != 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("refused backups left files behind: %v", names)
	}
	if _, err := (Backuper{}).Backup(ctx, good, "backups", at); !errors.Is(err, ErrBackup) {
		t.Errorf("relative backup dir: %v", err)
	}
	// An existing destination is never overwritten.
	if _, err := (Backuper{}).Backup(ctx, good, backups, at); err != nil {
		t.Fatal(err)
	}
	if _, err := (Backuper{}).Backup(ctx, good, backups, at); !errors.Is(err, ErrBackup) || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("same second twice: %v", err)
	}
	// No sqlite3: a clear error, nothing written.
	if _, err := (Backuper{SQLite: filepath.Join(dir, "no-such-sqlite3")}).Backup(ctx, good, filepath.Join(dir, "b2"), at); !errors.Is(err, ErrBackup) {
		t.Errorf("missing sqlite3: %v", err)
	}
	// A cancelled context stops it.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := (Backuper{}).Backup(cctx, good, filepath.Join(dir, "b3"), at); !errors.Is(err, ErrBackup) {
		t.Errorf("cancelled: %v", err)
	}
}

func TestBackupKeepsTheNewestAndOnlyItsOwnFiles(t *testing.T) {
	sqlite := sqliteOrSkip(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "career.db")
	makeCareerDB(t, sqlite, db, "a1")
	backups := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 12; i++ {
		name := fmt.Sprintf("career-202610%02d-120000.db", i)
		if err := os.WriteFile(filepath.Join(backups, name), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, other := range []string{"notes.txt", "career-latest.db", "career-20261001-120000.db.bak"} {
		if err := os.WriteFile(filepath.Join(backups, other), []byte("mine"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	info, err := Backuper{}.Backup(context.Background(), db, backups, time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if info.PruneErr != nil || info.Pruned != 3 {
		t.Fatalf("pruned %d (%v), want 3 of the 13 matching files", info.Pruned, info.PruneErr)
	}
	ents, _ := os.ReadDir(backups)
	n := 0
	for _, e := range ents {
		if backupNameRe.MatchString(e.Name()) {
			n++
		}
	}
	if n != KeepBackups {
		t.Fatalf("%d backups kept, want %d", n, KeepBackups)
	}
	for _, other := range []string{"notes.txt", "career-latest.db", "career-20261001-120000.db.bak"} {
		if _, err := os.Stat(filepath.Join(backups, other)); err != nil {
			t.Errorf("%s was pruned: %v", other, err)
		}
	}
	if _, err := os.Stat(info.Path); err != nil {
		t.Fatal("the new backup was pruned")
	}
}

func TestSQLiteURIEscapes(t *testing.T) {
	got := sqliteURI("/a b/c%d?e#f.db", "mode=rw")
	if got != "file:///a%20b/c%25d%3fe%23f.db?mode=rw" {
		t.Fatal(got)
	}
}
