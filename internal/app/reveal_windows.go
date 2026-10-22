//go:build windows

package app

import "os/exec"

// openInExplorer shows a folder in Windows Explorer.
func openInExplorer(dir string) error {
	// Explorer returns a non-zero exit code even on success, so its status is
	// deliberately not treated as a failure.
	_ = explorerCommand(dir).Start()
	return nil
}

// explorerCommand builds the Explorer call, with no HideWindow since
// Explorer passes it on and opens the folder in an invisible window
func explorerCommand(dir string) *exec.Cmd {
	return exec.Command("explorer", dir)
}
