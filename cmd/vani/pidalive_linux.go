//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// linuxUserHz is the clock tick /proc/pid/stat starttime is counted in.
// The exported Linux ABI uses 100, independent of the hardware tick.
const linuxUserHz = 100

func readProcIdentity(pid int) procIdentity {
	if pid <= 0 {
		return procIdentity{}
	}
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil || exe == "" || strings.HasSuffix(exe, " (deleted)") {
		return procIdentity{}
	}
	started, ok := linuxStart(pid)
	if !ok {
		return procIdentity{}
	}
	return procIdentity{Exe: exe, Started: started, OK: true}
}

func linuxStart(pid int) (time.Time, bool) {
	stat, err := readLimited(fmt.Sprintf("/proc/%d/stat", pid), 1<<16)
	if err != nil {
		return time.Time{}, false
	}
	ticks, ok := linuxStartTicks(stat)
	if !ok {
		return time.Time{}, false
	}
	boot, ok := linuxBootTime()
	if !ok {
		return time.Time{}, false
	}
	sec := boot + ticks/linuxUserHz
	nsec := (ticks % linuxUserHz) * (int64(time.Second) / linuxUserHz)
	if sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(sec, nsec).UTC(), true
}

// linuxStartTicks reads field 22 of a /proc/pid/stat line. The command name
// is in parentheses and may contain spaces; the field count starts after the
// last ')'.
func linuxStartTicks(stat []byte) (int64, bool) {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 || i+2 >= len(stat) {
		return 0, false
	}
	fields := bytes.Fields(stat[i+2:])
	if len(fields) < 20 {
		return 0, false
	}
	ticks, err := strconv.ParseInt(string(fields[19]), 10, 64)
	if err != nil || ticks < 0 {
		return 0, false
	}
	return ticks, true
}

func linuxBootTime() (int64, bool) {
	raw, err := readLimited("/proc/stat", 1<<20)
	if err != nil {
		return 0, false
	}
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		rest, ok := bytes.CutPrefix(line, []byte("btime "))
		if !ok {
			continue
		}
		sec, err := strconv.ParseInt(string(bytes.TrimSpace(rest)), 10, 64)
		if err != nil || sec <= 0 {
			return 0, false
		}
		return sec, true
	}
	return 0, false
}

func readLimited(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errors.New("proc file exceeds the read limit")
	}
	return b, nil
}
