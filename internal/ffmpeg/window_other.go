//go:build !windows

package ffmpeg

import (
	"os/exec"
	"syscall"
)

// hideWindow is a no-op on platforms without detachable consoles.
func hideWindow(cmd *exec.Cmd) {
	_ = cmd
}

// prepareTree starts the command in a process group of its own, so a cancel
// can end it together with everything it starts, as yt-dlp starts ffmpeg
func prepareTree(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// processTree is the process group a command leads
type processTree struct {
	pgid int
}

// adoptTree names the group prepareTree made, led by the command itself
func adoptTree(cmd *exec.Cmd) (*processTree, error) {
	return &processTree{pgid: cmd.Process.Pid}, nil
}

// kill ends every process in the group
func (t *processTree) kill() error {
	return syscall.Kill(-t.pgid, syscall.SIGKILL)
}

// release has nothing to close for a process group
func (t *processTree) release() {}

// exeSuffix is the executable file extension on this platform.
func exeSuffix() string {
	return ""
}
