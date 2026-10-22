//go:build windows

package ffmpeg

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// createNoWindow suppresses the console window Windows would otherwise flash
// for each child process
// This and the process tree below are the package's one justified
// conditional build (Power of Ten rule 8)
const createNoWindow = 0x08000000

// hideWindow prevents a console window appearing for cmd.
func hideWindow(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}

// prepareTree has nothing to set before start on Windows, the job object is
// attached once the process exists
func prepareTree(cmd *exec.Cmd) {
	_ = cmd
}

// processTree is a job object holding a command and everything it starts
type processTree struct {
	job windows.Handle
}

// adoptTree puts a started command in a job object that ends its whole tree
// when killed or closed, since yt-dlp runs a child of its own and ffmpeg,
// which otherwise outlive a cancel
// A child started in the instant before the assignment can escape, so Run
// also closes the pipes after a grace period
func adoptTree(cmd *exec.Cmd) (*processTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false,
		uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	return &processTree{job: job}, nil
}

// kill ends every process in the tree
func (t *processTree) kill() error {
	return windows.TerminateJobObject(t.job, 1)
}

// release closes the job, which also ends anything still left in it
func (t *processTree) release() {
	_ = windows.CloseHandle(t.job)
}

// exeSuffix is the executable file extension on this platform.
func exeSuffix() string {
	return ".exe"
}
