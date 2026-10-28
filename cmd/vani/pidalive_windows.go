//go:build windows

package main

import (
	"math"
	"syscall"
	"time"
	"unsafe"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259 // STILL_ACTIVE
)

// queryFullProcessImageNameW returns the Win32 path of a process opened with
// PROCESS_QUERY_LIMITED_INFORMATION. It is not wrapped by the standard library.
var queryFullProcessImageNameW = syscall.NewLazyDLL("kernel32.dll").NewProc("QueryFullProcessImageNameW")

// processExists reports whether this process can see pid. ERROR_ACCESS_DENIED
// means the pid may exist and this process may not inspect it; that is not
// visibility.
func processExists(pid int) bool {
	if pid <= 0 || pid > math.MaxUint32 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return true // it opened, so it is there; its state is unknown
	}
	return code == stillActive
}

func readProcIdentity(pid int) procIdentity {
	if pid <= 0 || pid > math.MaxUint32 {
		return procIdentity{}
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return procIdentity{}
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil || code != stillActive {
		return procIdentity{}
	}
	exe, err := processImagePath(h)
	if err != nil || exe == "" {
		return procIdentity{}
	}
	var created, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &created, &exit, &kernel, &user); err != nil {
		return procIdentity{}
	}
	started := time.Unix(0, created.Nanoseconds()).UTC()
	// A zero FILETIME is year 1601. That is not a start time we can trust.
	if !started.After(time.Unix(0, 0)) {
		return procIdentity{}
	}
	return procIdentity{Exe: exe, Started: started, OK: true}
}

func processImagePath(h syscall.Handle) (string, error) {
	n := uint32(32768)
	buf := make([]uint16, n)
	r1, _, e := queryFullProcessImageNameW.Call(
		uintptr(h),
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&n)),
	)
	if r1 == 0 {
		if e != nil {
			return "", e
		}
		return "", syscall.EINVAL
	}
	return syscall.UTF16ToString(buf[:n]), nil
}
