//go:build darwin

package main

import (
	"bytes"
	"encoding/binary"
	"syscall"
	"time"
	"unsafe"
)

const (
	// CTL_KERN and KERN_PROCARGS2 from sys/sysctl.h. The reply is an int32
	// argc followed by the executable path.
	ctlKern       = 1
	kernProcArgs2 = 49
	// PROC_INFO_CALL_PIDINFO and PROC_PIDTBSDINFO from libproc. proc_bsdinfo
	// is 136 bytes: pbi_pid at 12, pbi_start_tvsec at 120, pbi_start_tvusec at 128.
	procInfoPIDInfo = 2
	procPIDTBSDInfo = 3
	bsdInfoSize     = 136
	maxProcArgs     = 1 << 20
)

func readProcIdentity(pid int) procIdentity {
	if pid <= 0 || pid > 0x7fffffff {
		return procIdentity{}
	}
	exe, ok := darwinExe(pid)
	if !ok {
		return procIdentity{}
	}
	started, ok := darwinStart(pid)
	if !ok {
		return procIdentity{}
	}
	return procIdentity{Exe: exe, Started: started, OK: true}
}

func darwinExe(pid int) (string, bool) {
	buf, err := sysctlBytes([]int32{ctlKern, kernProcArgs2, int32(pid)})
	if err != nil || len(buf) < 5 {
		return "", false
	}
	path, ok := cString(buf[4:])
	if !ok || len(path) == 0 || path[0] != '/' {
		return "", false
	}
	return path, true
}

func darwinStart(pid int) (time.Time, bool) {
	buf := make([]byte, 256)
	n, err := procPIDInfo(pid, procPIDTBSDInfo, buf)
	if err != nil || n < bsdInfoSize {
		return time.Time{}, false
	}
	buf = buf[:bsdInfoSize]
	got := int(binary.LittleEndian.Uint32(buf[12:16]))
	if got != pid {
		return time.Time{}, false
	}
	sec := int64(binary.LittleEndian.Uint64(buf[120:128]))
	usec := int64(binary.LittleEndian.Uint64(buf[128:136]))
	if sec <= 0 || usec < 0 || usec >= 1_000_000 {
		return time.Time{}, false
	}
	return time.Unix(sec, usec*int64(time.Microsecond)).UTC(), true
}

func procPIDInfo(pid, flavor int, buf []byte) (int, error) {
	r1, _, e := syscall.Syscall6(syscall.SYS_PROC_INFO,
		uintptr(procInfoPIDInfo), uintptr(pid), uintptr(flavor), 0,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if e != 0 {
		return 0, e
	}
	if int(r1) <= 0 {
		return 0, syscall.EIO
	}
	return int(r1), nil
}

func sysctlBytes(mib []int32) ([]byte, error) {
	var n uintptr
	_, _, e := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		0, uintptr(unsafe.Pointer(&n)), 0, 0)
	if e != 0 {
		return nil, e
	}
	if n == 0 || n > maxProcArgs {
		return nil, syscall.EIO
	}
	buf := make([]byte, n)
	_, _, e = syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), uintptr(len(mib)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
	if e != 0 {
		return nil, e
	}
	if n == 0 || n > uintptr(len(buf)) {
		return nil, syscall.EIO
	}
	return buf[:n], nil
}

func cString(b []byte) (string, bool) {
	i := bytes.IndexByte(b, 0)
	if i <= 0 {
		return "", false
	}
	return string(b[:i]), true
}
