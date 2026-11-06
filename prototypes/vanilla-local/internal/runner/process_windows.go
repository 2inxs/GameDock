package runner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"
)

const (
	createSuspended              = 0x00000004
	createNoWindow               = 0x08000000
	processTerminate             = 0x0001
	processSetQuota              = 0x0100
	threadSuspendResume          = 0x0002
	snapshotThreads              = 0x00000004
	jobObjectExtendedLimitInfo   = 9
	jobObjectLimitKillOnJobClose = 0x00002000
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	createJobObject          = kernel32.NewProc("CreateJobObjectW")
	setJobObjectInformation  = kernel32.NewProc("SetInformationJobObject")
	assignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	threadFirst              = kernel32.NewProc("Thread32First")
	threadNext               = kernel32.NewProc("Thread32Next")
	openThread               = kernel32.NewProc("OpenThread")
	resumeThread             = kernel32.NewProc("ResumeThread")
)

type processJob struct {
	handle syscall.Handle
}

func (j *processJob) close() {
	if j.handle != 0 {
		syscall.CloseHandle(j.handle)
		j.handle = 0
	}
}

// extendedLimitInfo mirrors JOBOBJECT_EXTENDED_LIMIT_INFORMATION.
type extendedLimitInfo struct {
	perProcessUserTimeLimit int64
	perJobUserTimeLimit     int64
	limitFlags              uint32
	minimumWorkingSetSize   uintptr
	maximumWorkingSetSize   uintptr
	activeProcessLimit      uint32
	affinity                uintptr
	priorityClass           uint32
	schedulingClass         uint32
	_                       [8 - unsafe.Sizeof(uintptr(0))]byte // Align the basic limit structure on 32-bit Windows.
	ioCounters              [6]uint64
	processMemoryLimit      uintptr
	jobMemoryLimit          uintptr
	peakProcessMemoryUsed   uintptr
	peakJobMemoryUsed       uintptr
}

func startProcess(ctx context.Context, cmd *exec.Cmd) (processJob, error) {
	handle, _, callErr := createJobObject.Call(0, 0)
	if handle == 0 {
		return processJob{}, fmt.Errorf("create Windows job: %w", callErr)
	}
	job := processJob{handle: syscall.Handle(handle)}
	limits := extendedLimitInfo{limitFlags: jobObjectLimitKillOnJobClose}
	result, _, callErr := setJobObjectInformation.Call(handle, jobObjectExtendedLimitInfo,
		uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits))
	if result == 0 {
		job.close()
		return processJob{}, fmt.Errorf("configure Windows job: %w", callErr)
	}

	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createSuspended | createNoWindow}
	if err := cmd.Start(); err != nil {
		job.close()
		return processJob{}, fmt.Errorf("launch Java: %w", err)
	}
	pid := uint32(cmd.Process.Pid)
	if err := job.attach(pid); err != nil {
		job.close()
		cmd.Process.Kill()
		cmd.Wait()
		return processJob{}, err
	}
	if err := ctx.Err(); err != nil {
		job.close()
		cmd.Wait()
		return processJob{}, err
	}
	if err := resumePrimaryThread(pid); err != nil {
		job.close()
		cmd.Wait()
		return processJob{}, err
	}
	return job, nil
}

func (j processJob) attach(pid uint32) error {
	process, err := syscall.OpenProcess(processSetQuota|processTerminate, false, pid)
	if err != nil {
		return fmt.Errorf("open Java process: %w", err)
	}
	defer syscall.CloseHandle(process)
	if result, _, err := assignProcessToJobObject.Call(uintptr(j.handle), uintptr(process)); result == 0 {
		return fmt.Errorf("attach Java to Windows job: %w", err)
	}
	return nil
}

type threadEntry struct {
	size, usage, id, processID, basePriority, deltaPriority, flags uint32
}

func resumePrimaryThread(pid uint32) error {
	snapshot, err := syscall.CreateToolhelp32Snapshot(snapshotThreads, 0)
	if err != nil {
		return fmt.Errorf("snapshot Windows threads: %w", err)
	}
	defer syscall.CloseHandle(snapshot)
	entry := threadEntry{}
	entry.size = uint32(unsafe.Sizeof(entry))
	result, _, callErr := threadFirst.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	for result != 0 {
		if entry.size >= 16 && entry.processID == pid {
			thread, _, err := openThread.Call(threadSuspendResume, 0, uintptr(entry.id))
			if thread == 0 {
				return fmt.Errorf("open Java thread: %w", err)
			}
			defer syscall.CloseHandle(syscall.Handle(thread))
			count, _, err := resumeThread.Call(thread)
			if uint32(count) == ^uint32(0) {
				return fmt.Errorf("resume Java thread: %w", err)
			}
			if count > 1 {
				return errors.New("Java thread was still suspended after resuming")
			}
			return nil
		}
		entry.size = uint32(unsafe.Sizeof(entry))
		result, _, callErr = threadNext.Call(uintptr(snapshot), uintptr(unsafe.Pointer(&entry)))
	}
	return fmt.Errorf("find Java thread: %w", callErr)
}
