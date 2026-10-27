//go:build windows

package rkbexec

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Windows has no process group to signal. rkb fit starts a page renderer that
// outlives rkb, so the child is created suspended, assigned to a job with
// kill-on-close, and only then resumed. Cancel terminates that job. js and
// wasip1 do not take this path.

const (
	createSuspended = 0x00000004 // CREATE_SUSPENDED

	processSetQuota = 0x0100 // PROCESS_SET_QUOTA, required by AssignProcessToJobObject

	threadSuspendResume = 0x0002 // THREAD_SUSPEND_RESUME

	jobObjectLimitKillOnJobClose             = 0x00002000
	jobObjectExtendedLimitInformation        = 9
	jobObjectBasicProcessIdList              = 3
	stillActive                       uint32 = 259
	errorInvalidHandle                       = syscall.Errno(6)
	errorInvalidParameter                    = syscall.Errno(87)
	errorNoMoreFiles                         = syscall.Errno(18)
)

var (
	kernel32                      = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW          = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject   = kernel32.NewProc("SetInformationJobObject")
	procQueryInformationJobObject = kernel32.NewProc("QueryInformationJobObject")
	procAssignProcessToJobObject  = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject        = kernel32.NewProc("TerminateJobObject")
	procOpenThread                = kernel32.NewProc("OpenThread")
	procResumeThread              = kernel32.NewProc("ResumeThread")
	procThread32First             = kernel32.NewProc("Thread32First")
	procThread32Next              = kernel32.NewProc("Thread32Next")
)

// Layout matches the Windows ABI (the same field order as the system headers).
// On 64-bit, basic is 64 bytes and extended is 144; init checks that.
type jobBasicLimit struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobExtendedLimit struct {
	Basic                 jobBasicLimit
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type jobPIDList struct {
	NumberOfAssignedProcesses uint32
	NumberOfProcessIdsInList  uint32
	ProcessIdList             [32]uintptr
}

type threadEntry32 struct {
	Size           uint32
	Usage          uint32
	ThreadID       uint32
	OwnerProcessID uint32
	BasePri        int32
	DeltaPri       int32
	Flags          uint32
}

func init() {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return
	}
	var info jobExtendedLimit
	if unsafe.Sizeof(info.Basic) != 64 || unsafe.Offsetof(info.IoInfo) != 64 || unsafe.Sizeof(info) != 144 {
		panic("rkbexec: job object struct does not match the 64-bit windows ABI")
	}
}

// group is the job for one child. mu serializes cancel with adopt so the
// process cannot run outside the job: either it is killed while still
// suspended, or it is in the job before it is resumed.
type group struct {
	mu     sync.Mutex
	job    syscall.Handle
	killed bool
}

var (
	groupMu sync.Mutex
	groups  = map[*exec.Cmd]*group{}
)

func configureProcess(cmd *exec.Cmd) error {
	if cmd == nil {
		return nil
	}
	job, err := createKillJob()
	if err != nil {
		return err
	}
	g := &group{job: job}
	groupMu.Lock()
	groups[cmd] = g
	groupMu.Unlock()
	attr := &syscall.SysProcAttr{}
	if cmd.SysProcAttr != nil {
		attr = cmd.SysProcAttr
	}
	attr.CreationFlags |= createSuspended
	cmd.SysProcAttr = attr
	cmd.Cancel = func() error { return g.stop(cmd.Process) }
	return nil
}

func createKillJob() (syscall.Handle, error) {
	r, _, err := procCreateJobObjectW.Call(0, 0)
	if r == 0 {
		return 0, callErr(err)
	}
	job := syscall.Handle(r)
	info := jobExtendedLimit{}
	info.Basic.LimitFlags = jobObjectLimitKillOnJobClose
	if err := setJobInfo(job, &info); err != nil {
		_ = syscall.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

func adoptGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	g := lookupGroup(cmd)
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job == 0 || processGone(cmd.Process.Pid) {
		return nil
	}
	h, err := syscall.OpenProcess(processSetQuota|syscall.PROCESS_TERMINATE|syscall.PROCESS_QUERY_INFORMATION, false, uint32(cmd.Process.Pid))
	if err != nil {
		if processGone(cmd.Process.Pid) || goneErr(err) {
			return nil
		}
		return g.fail(err)
	}
	defer syscall.CloseHandle(h)
	if err := assignJob(g.job, h); err != nil {
		if processGone(cmd.Process.Pid) || goneErr(err) {
			return nil
		}
		return g.fail(err)
	}
	if err := resumeProcessThread(uint32(cmd.Process.Pid)); err != nil {
		if processGone(cmd.Process.Pid) || goneErr(err) {
			return nil
		}
		return g.fail(err)
	}
	if g.killed {
		_ = terminateJob(g.job)
		_ = cmd.Process.Kill()
	}
	return nil
}

// fail marks the tree for kill-on-close and terminates it. The caller holds g.mu.
func (g *group) fail(err error) error {
	g.killed = true
	if g.job != 0 {
		_ = terminateJob(g.job)
	}
	return err
}

func (g *group) stop(proc *os.Process) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.killed = true
	var termErr error
	if g.job != 0 {
		termErr = terminateJob(g.job)
		if goneErr(termErr) {
			termErr = nil
		}
	}
	if proc == nil {
		if termErr != nil {
			return termErr
		}
		return os.ErrProcessDone
	}
	// Kill the direct process too: it may still be suspended and not yet in
	// the job. Already-gone is not a failure, matching the unix ESRCH path.
	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) && !processGone(proc.Pid) {
		if termErr != nil {
			return termErr
		}
		return err
	}
	if processGone(proc.Pid) && termErr != nil {
		return os.ErrProcessDone
	}
	if termErr != nil {
		return termErr
	}
	if processGone(proc.Pid) {
		return os.ErrProcessDone
	}
	return nil
}

func reapGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	g := lookupGroup(cmd)
	if g == nil {
		return
	}
	deadline := time.Now().Add(groupGrace)
	for {
		g.mu.Lock()
		job := g.job
		g.mu.Unlock()
		if job == 0 || !jobHasProcess(job) {
			return
		}
		if time.Now().After(deadline) {
			g.mu.Lock()
			if g.job != 0 {
				_ = terminateJob(g.job)
				g.killed = true
			}
			g.mu.Unlock()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func dropGroup(cmd *exec.Cmd) {
	g := takeGroup(cmd)
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.job == 0 {
		return
	}
	// A run that finished on its own must not kill a child rkb already reaped
	// when this handle closes. Cancel and adopt failure set killed.
	if !g.killed {
		clearKillOnClose(g.job)
	}
	_ = syscall.CloseHandle(g.job)
	g.job = 0
}

func lookupGroup(cmd *exec.Cmd) *group {
	if cmd == nil {
		return nil
	}
	groupMu.Lock()
	defer groupMu.Unlock()
	return groups[cmd]
}

func takeGroup(cmd *exec.Cmd) *group {
	if cmd == nil {
		return nil
	}
	groupMu.Lock()
	defer groupMu.Unlock()
	g := groups[cmd]
	delete(groups, cmd)
	return g
}

func setJobInfo(job syscall.Handle, info *jobExtendedLimit) error {
	r, _, err := procSetInformationJobObject.Call(
		uintptr(job),
		jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(info)),
		uintptr(unsafe.Sizeof(*info)),
	)
	return callOK(r, err)
}

func assignJob(job syscall.Handle, proc syscall.Handle) error {
	r, _, err := procAssignProcessToJobObject.Call(uintptr(job), uintptr(proc))
	return callOK(r, err)
}

func terminateJob(job syscall.Handle) error {
	r, _, err := procTerminateJobObject.Call(uintptr(job), 1)
	return callOK(r, err)
}

func clearKillOnClose(job syscall.Handle) {
	var info jobExtendedLimit
	r, _, _ := procQueryInformationJobObject.Call(
		uintptr(job),
		jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uintptr(unsafe.Sizeof(info)),
		0,
	)
	if r == 0 {
		return
	}
	info.Basic.LimitFlags &^= jobObjectLimitKillOnJobClose
	_ = setJobInfo(job, &info)
}

func jobHasProcess(job syscall.Handle) bool {
	var list jobPIDList
	r, _, err := procQueryInformationJobObject.Call(
		uintptr(job),
		jobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&list)),
		uintptr(unsafe.Sizeof(list)),
		0,
	)
	if list.NumberOfAssignedProcesses > 0 {
		return true
	}
	return r == 0 && errors.Is(err, syscall.ERROR_MORE_DATA)
}

func resumeProcessThread(pid uint32) error {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(snap)
	var te threadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	if err := thread32(procThread32First, snap, &te); err != nil {
		return err
	}
	for {
		if te.OwnerProcessID == pid {
			return resumeThread(te.ThreadID)
		}
		err := thread32(procThread32Next, snap, &te)
		if errors.Is(err, errorNoMoreFiles) {
			return errors.New("rkb child has no suspended thread")
		}
		if err != nil {
			return err
		}
	}
}

func thread32(proc *syscall.LazyProc, snap syscall.Handle, te *threadEntry32) error {
	r, _, err := proc.Call(uintptr(snap), uintptr(unsafe.Pointer(te)))
	return callOK(r, err)
}

func resumeThread(tid uint32) error {
	r, _, err := procOpenThread.Call(threadSuspendResume, 0, uintptr(tid))
	if r == 0 {
		return callErr(err)
	}
	h := syscall.Handle(r)
	defer syscall.CloseHandle(h)
	prev, _, err := procResumeThread.Call(uintptr(h))
	if prev == ^uintptr(0) {
		return callErr(err)
	}
	return nil
}

func processGone(pid int) bool {
	if pid <= 0 {
		return true
	}
	h, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return goneErr(err)
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code != stillActive
}

func goneErr(err error) bool {
	return errors.Is(err, errorInvalidHandle) || errors.Is(err, errorInvalidParameter)
}

func callOK(r uintptr, err error) error {
	if r != 0 {
		return nil
	}
	return callErr(err)
}

func callErr(err error) error {
	if err != nil && err != syscall.Errno(0) {
		return err
	}
	return syscall.EINVAL
}
