//go:build !windows

package app

import (
	"os/exec"
	"runtime"
)

// openInExplorer shows a folder in the platform file manager.
func openInExplorer(dir string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	return exec.Command(name, dir).Start()
}
