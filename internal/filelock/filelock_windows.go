//go:build windows

package filelock

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// The syscall package does not wrap LockFileEx, so it is called from
// kernel32 directly. One byte at offset 0 is locked: a range past the end of
// the file is allowed, so an empty lock file works.
var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2

	errorLockViolation syscall.Errno = 33 // ERROR_LOCK_VIOLATION
)

// TryLock takes a shared or exclusive lock on f's first byte without waiting.
func TryLock(f *os.File, exclusive bool) error {
	flags := uintptr(lockfileFailImmediately)
	if exclusive {
		flags |= lockfileExclusiveLock
	}
	var ol syscall.Overlapped
	r, _, err := procLockFileEx.Call(f.Fd(), flags, 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r != 0 {
		return nil
	}
	if errors.Is(err, errorLockViolation) || errors.Is(err, syscall.ERROR_IO_PENDING) {
		return ErrWouldBlock
	}
	return err
}

// Unlock releases the lock TryLock took on f.
func Unlock(f *os.File) error {
	var ol syscall.Overlapped
	r, _, err := procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&ol)))
	if r != 0 {
		return nil
	}
	return err
}
