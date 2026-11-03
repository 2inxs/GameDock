package network

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeConfigAndNoTunTraffic(t *testing.T) {
	core, cli := nativePaths(t)
	c := testRoom()
	c.GamePort = freePort(t)
	game, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", c.GamePort))
	if err != nil {
		t.Fatal(err)
	}
	defer game.Close()
	go func() {
		for {
			connection, err := game.Accept()
			if err != nil {
				return
			}
			go func() { defer connection.Close(); io.Copy(connection, connection) }()
		}
	}()
	listenerPort := freePort(t)
	opts := Options{CorePath: core, CLIPath: cli, RuntimeDir: t.TempDir(), listeners: []string{fmt.Sprintf("tcp://127.0.0.1:%d", listenerPort)}, pollInterval: 100 * time.Millisecond}
	host := New(opts)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := host.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	// Use the native validator before launch so schema incompatibilities are explicit.
	path := filepath.Join(t.TempDir(), "check.toml")
	if err = os.WriteFile(path, []byte(coreConfig(c, opts.listeners)), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := host.command(context.Background(), core, "--disable-env-parsing", "--config-file", path, "--check-config")
	if err != nil {
		t.Fatalf("native validator: %v (%s)", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err = host.Connect(ctx, c); err != nil {
		t.Fatalf("host start: %v, %+v", err, host.Status())
	}
	guestConfig := c
	guestConfig.ID = strings.Repeat("b", 32)
	guestConfig.Role = "guest"
	guestConfig.LocalPort = freePort(t)
	guestConfig.BootstrapPeers = []string{fmt.Sprintf("tcp://127.0.0.1:%d", listenerPort)}
	guest := New(Options{CorePath: core, CLIPath: cli, RuntimeDir: t.TempDir(), listeners: []string{"tcp://127.0.0.1:0"}, pollInterval: 100 * time.Millisecond})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := guest.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	if err = guest.Connect(ctx, guestConfig); err != nil {
		t.Fatalf("guest start: %v, %+v", err, guest.Status())
	}
	deadline := time.Now().Add(15 * time.Second)
	for guest.Status().State != "connected" || guest.Status().VirtualIP == "" {
		if time.Now().After(deadline) {
			t.Fatalf("room not ready: host=%+v guest=%+v", host.Status(), guest.Status())
		}
		time.Sleep(100 * time.Millisecond)
	}
	connection, err := net.DialTimeout("tcp", guest.Status().Address, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(5 * time.Second))
	message := []byte("GameDock native no-TUN roundtrip")
	if _, err = connection.Write(message); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, len(message))
	if _, err = io.ReadFull(connection, response); err != nil {
		t.Fatalf("overlay echo: %v; host=%+v guest=%+v", err, host.Status(), guest.Status())
	}
	if string(response) != string(message) {
		t.Fatal("incorrect echo")
	}
	t.Logf("actual engine %s: no-TUN TCP roundtrip; guest=%s virtual=%s", EngineVersion, guest.Status().Address, guest.Status().VirtualIP)
	address := guest.Status().Address
	if err = guest.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond); err == nil {
		conn.Close()
		t.Fatal("local forwarding survived disconnect")
	}
}
