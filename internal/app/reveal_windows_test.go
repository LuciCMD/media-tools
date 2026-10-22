//go:build windows

package app

import "testing"

// TestExplorerOpensVisibly checks Show in Folder doesn't start Explorer with
// HideWindow, which Explorer passes on to the folder window it opens
func TestExplorerOpensVisibly(t *testing.T) {
	cmd := explorerCommand(`C:\`)
	if cmd.SysProcAttr != nil && cmd.SysProcAttr.HideWindow {
		t.Error("Explorer is started hidden, so the folder opens in an invisible window")
	}
}
