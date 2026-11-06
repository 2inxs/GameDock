package runner

import (
	"strings"
	"testing"
)

func TestReadySurvivesConsoleRotation(t *testing.T) {
	log := &console{}
	log.Write([]byte("[12:00:00] [Server thread/INFO]: Done (1.2s)! For help, type \"help\"\r\n"))
	log.Write([]byte(strings.Repeat("ordinary line\n", consoleLines+1)))
	lines, ready := log.snapshot()
	if !ready || len(lines) != consoleLines || lines[0] != "ordinary line" {
		t.Fatalf("rotation lost readiness or did not discard old output: %v (%d lines)", ready, len(lines))
	}
}
