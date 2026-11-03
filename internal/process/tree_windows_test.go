//go:build windows

package process

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"gamedock/internal/model"
	"golang.org/x/sys/windows"
)

func TestWindowsJobTerminatesDescendants(t *testing.T) {
	for _, method := range []string{"stop", "kill"} {
		t.Run(method, func(t *testing.T) {
			m := newTestManager(t)
			if _, err := m.Start("tree", helper("normal")); err != nil {
				t.Fatal(err)
			}
			// Spawn after Start returns, when the root is already assigned to its job.
			if err := m.SendCommand("tree", "spawn"); err != nil {
				t.Fatal(err)
			}
			var pid int
			eventually(t, func() bool {
				for _, entry := range m.Logs("tree", 0) {
					if strings.HasPrefix(entry.Message, "child:") {
						pid, _ = strconv.Atoi(strings.TrimPrefix(entry.Message, "child:"))
						return pid > 0
					}
				}
				return false
			})
			handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(handle)
			if method == "stop" {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if err := m.Stop(ctx, "tree"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := m.Kill("tree"); err != nil {
					t.Fatal(err)
				}
				eventually(t, func() bool { return m.Snapshot("tree").State == model.Stopped })
			}
			status, err := windows.WaitForSingleObject(handle, 1000)
			if err != nil || status != windows.WAIT_OBJECT_0 {
				t.Fatalf("descendant still alive: wait=%d err=%v", status, err)
			}
		})
	}
}
