//go:build linux

package main

import (
	"testing"
	"time"
)

func TestLinuxStartTicks(t *testing.T) {
	// Field 22 is starttime. The comm field holds spaces and a ')'.
	stat := []byte("12 (a) b) S 1 1 1 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 250 0 0")
	ticks, ok := linuxStartTicks(stat)
	if !ok || ticks != 250 {
		t.Fatalf("ticks %d ok %v", ticks, ok)
	}
	if _, ok := linuxStartTicks([]byte("no paren")); ok {
		t.Fatal("a line with no comm parsed")
	}
	if _, ok := linuxStartTicks([]byte("1 (short) S 1")); ok {
		t.Fatal("a short stat parsed")
	}
	boot := int64(1_700_000_000)
	sec := boot + 250/linuxUserHz
	nsec := (250 % linuxUserHz) * (int64(time.Second) / linuxUserHz)
	if sec != boot+2 || nsec != 500*int64(time.Millisecond) {
		t.Fatalf("start %d.%d", sec, nsec)
	}
}
