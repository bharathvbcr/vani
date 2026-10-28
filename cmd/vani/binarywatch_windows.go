//go:build windows

package main

import (
	"io/fs"
	"os"
	"syscall"
	"unsafe"
)

// kernel32 is called directly, the same way internal/filelock locks a file.
// os.FileInfo.Sys on Windows is *syscall.Win32FileAttributeData, which has no
// file index, so the index is read from the open handle. golang.org/x/sys is
// not used.
var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procCreateFileW                = kernel32.NewProc("CreateFileW")
	procGetFileInformationByHandle = kernel32.NewProc("GetFileInformationByHandle")
)

// statFileID opens a regular file and reads its volume serial and file index.
// The caller has already returned a missing path and a non-regular file, so
// this does not open a directory or a pipe. CreateFileW is given
// FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE and OPEN_EXISTING, and
// not FILE_FLAG_OPEN_REPARSE_POINT, so it follows symlinks the way os.Stat
// did. The handle is closed on every path that opened one. A failed open or
// query is returned: an unreadable path is not a replacement.
func statFileID(path string, _ fs.FileInfo) (dev, ino uint64, err error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}
	// Flags are DWORD arguments. FILE_ATTRIBUTE_NORMAL is not
	// FILE_FLAG_OPEN_REPARSE_POINT (0x00200000).
	h, _, callErr := procCreateFileW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(syscall.GENERIC_READ),
		uintptr(syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE),
		0,
		uintptr(syscall.OPEN_EXISTING),
		uintptr(syscall.FILE_ATTRIBUTE_NORMAL),
		0,
	)
	if h == uintptr(syscall.InvalidHandle) {
		return 0, 0, windowsCallErr("CreateFileW", path, callErr)
	}
	defer func() {
		if cerr := syscall.CloseHandle(syscall.Handle(h)); cerr != nil && err == nil {
			err = windowsCallErr("CloseHandle", path, cerr)
		}
	}()
	// Go 1.26.6 syscall.ByHandleFileInformation is BY_HANDLE_FILE_INFORMATION:
	// DWORD and FILETIME fields, no pointers, the same layout on 386 and amd64
	// (src/syscall/types_windows.go).
	var info syscall.ByHandleFileInformation
	r, _, callErr := procGetFileInformationByHandle.Call(h, uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 0, 0, windowsCallErr("GetFileInformationByHandle", path, callErr)
	}
	dev, ino = packWindowsFileID(info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow)
	return dev, ino, nil
}

// windowsCallErr turns a failed kernel32 call into a path error. Call's error
// is documented to be non-nil, but a nil here would look like a successful
// stat of a zero identity, so it is not returned as success.
func windowsCallErr(op, path string, err error) error {
	if err == nil {
		err = syscall.EINVAL
	}
	return &os.PathError{Op: op, Path: path, Err: err}
}
