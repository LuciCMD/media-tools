package tools

import (
	"os/exec"
	"strings"
	"testing"
)

// processesMentioning lists the PID and name of every process whose command
// line holds marker, a temp folder only this test uses
func processesMentioning(t *testing.T, marker string) []string {
	t.Helper()
	script := "Get-CimInstance Win32_Process | Where-Object { $_.CommandLine -and $_.CommandLine.Contains('" +
		strings.ReplaceAll(marker, "'", "''") + "') -and $_.ProcessId -ne $PID } | " +
		`ForEach-Object { "$($_.ProcessId) $($_.Name) $($_.CommandLine)" }`
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		t.Fatalf("listing processes: %v", err)
	}
	var found []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.Contains(strings.ToLower(line), "powershell") {
			found = append(found, line)
		}
	}
	return found
}
