package network

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gamedock/internal/rooms"
)

func testRoom() rooms.Config {
	return rooms.Config{ID: strings.Repeat("a", 32), Name: "Test room", NetworkName: "test-network", Secret: "safe-test-secret-123456789", Role: "host", HostVirtualIP: rooms.DefaultHostVirtualIP, GamePort: 25565, LocalPort: 25566, CreatedAt: time.Now()}
}
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}
func waitUntil(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition timed out")
}

func TestGeneratedConfigIsRestricted(t *testing.T) {
	for _, role := range []string{"host", "guest"} {
		c := testRoom()
		c.Role = role
		value := coreConfig(c, []string{"tcp://127.0.0.1:10000"})
		for _, want := range []string{"no_tun = true", "use_smoltcp = true", "accept_dns = false", "disable_upnp = true", "p2p_only = true", "disable_relay_data = true", "private_mode = true", "exit_nodes = []", "proxy_network = []", "default_action = 2"} {
			if !strings.Contains(value, want) {
				t.Errorf("%s missing %s", role, want)
			}
		}
		if role == "host" {
			if !strings.Contains(value, `ports = ["25565"]`) || strings.Contains(value, "[[port_forward]]") {
				t.Fatal("invalid host exposure")
			}
		} else {
			if strings.Contains(value, "minecraft-only") || !strings.Contains(value, `bind_addr = "127.0.0.1:25566"`) {
				t.Fatal("invalid guest exposure")
			}
		}
		c.AllowRelay = true
		if !strings.Contains(coreConfig(c, nil), "p2p_only = false") {
			t.Fatal("relay opt-in ignored")
		}
	}
}
func TestEnvironmentAndConfigEscaping(t *testing.T) {
	t.Setenv("ET_NO_TUN", "false")
	t.Setenv("ET_ACCEPT_DNS", "true")
	t.Setenv("GAMEDOCK_RETAIN_ENV", "yes")
	for _, v := range cleanEnvironment() {
		if strings.HasPrefix(strings.ToUpper(v), "ET_") {
			t.Fatal("inherited engine override")
		}
	}
	c := testRoom()
	c.Secret = `test"quoted\secret${HOME}-1234`
	value := coreConfig(c, nil)
	if !strings.Contains(value, "network_secret = "+quoted(c.Secret)) {
		t.Fatal("unescaped TOML string")
	}
}

func TestNetworkHelper(t *testing.T) {
	args := os.Args
	helper := false
	for _, a := range args {
		if a == "--network-helper" {
			helper = true
		}
	}
	if !helper {
		return
	}
	arg := func(name string) string {
		for i, v := range args {
			if v == name && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	for _, a := range args {
		if a == "--version" {
			fmt.Println("easytier 2.6.4")
			os.Exit(0)
		}
		if a == "--check-config" {
			os.Exit(0)
		}
	}
	if path := arg("--config-file"); path != "" {
		content, err := os.ReadFile(path)
		if err != nil {
			os.Exit(1)
		}
		if strings.Contains(string(content), `network_name = "slow-core"`) {
			time.Sleep(20 * time.Second)
			os.Exit(0)
		}
		if strings.Contains(string(content), `network_name = "crash-core"`) {
			os.Exit(8)
		}
		listener, err := net.Listen("tcp4", arg("--rpc-portal"))
		if err != nil {
			os.Exit(2)
		}
		for {
			connection, err := listener.Accept()
			if err != nil {
				os.Exit(3)
			}
			fmt.Fprint(connection, `[{"id":"1","hostname":"local","ipv4":"10.144.144.1","cost":"Local","lat_ms":"-"}]`)
			connection.Close()
		}
	}
	connection, err := net.DialTimeout("tcp", arg("--rpc-portal"), 100*time.Millisecond)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(4)
	}
	defer connection.Close()
	var rows []peerRow
	if err = json.NewDecoder(connection).Decode(&rows); err != nil {
		os.Exit(5)
	}
	json.NewEncoder(os.Stdout).Encode(rows)
	os.Exit(0)
}
func helperManager(t *testing.T) *Manager {
	t.Helper()
	m := New(Options{CorePath: os.Args[0], CLIPath: os.Args[0], RuntimeDir: t.TempDir(), argsPrefix: []string{"-test.run=^TestNetworkHelper$", "--", "--network-helper"}, pollInterval: 20 * time.Millisecond})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return m
}
func TestLifecycleAndDuplicateConnect(t *testing.T) {
	m := helperManager(t)
	c := testRoom()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- m.Connect(ctx, c) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else {
			t.Logf("connect error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful connects=%d", successes)
	}
	s := m.Status()
	if s.State != "waiting" || s.VirtualIP != c.HostVirtualIP {
		t.Fatalf("unexpected status %+v", s)
	}
	data, _ := json.Marshal(s)
	if strings.Contains(string(data), c.Secret) {
		t.Fatal("status leaked secret")
	}
	s.Logs[0] = "mutated"
	if m.Status().Logs[0] == "mutated" {
		t.Fatal("status shares slices")
	}
	if err := m.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if m.Status().State != "disconnected" {
		t.Fatal("did not disconnect")
	}
	entries, err := os.ReadDir(m.opts.RuntimeDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private config not removed: %v %v", entries, err)
	}
	if err := m.Connect(ctx, c); err != nil {
		t.Fatal(err)
	}
}

func TestHelperQuery(t *testing.T) {
	m := helperManager(t)
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err == nil {
			fmt.Fprint(c, `[{"id":"1","ipv4":"10.144.144.1","cost":"Local"}]`)
			c.Close()
		}
	}()
	output, err := m.command(context.Background(), m.opts.CLIPath, "--rpc-portal", l.Addr().String(), "--output", "json", "peer")
	t.Logf("query=%s error=%v", output, err)
	if err != nil {
		t.Fatal(err)
	}
}
func TestCancellationDuringStartup(t *testing.T) {
	m := helperManager(t)
	c := testRoom()
	c.NetworkName = "slow-core"
	result := make(chan error, 1)
	go func() { result <- m.Connect(context.Background(), c) }()
	waitUntil(t, func() bool { return m.Status().State == "connecting" })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled connect succeeded")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("connect did not terminate")
	}
	if m.Status().State != "disconnected" {
		t.Fatalf("state=%+v", m.Status())
	}
}
func TestTimeoutAndLaunchFailureCleanup(t *testing.T) {
	for _, mode := range []string{"slow-core", "crash-core"} {
		t.Run(mode, func(t *testing.T) {
			m := helperManager(t)
			c := testRoom()
			c.NetworkName = mode
			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			defer cancel()
			started := time.Now()
			if err := m.Connect(ctx, c); err == nil {
				t.Fatal("expected error")
			}
			if time.Since(started) > 4*time.Second {
				t.Fatal("unbounded cleanup")
			}
			if m.Status().State != "error" {
				t.Fatalf("state=%+v", m.Status())
			}
			entries, _ := os.ReadDir(m.opts.RuntimeDir)
			if len(entries) != 0 {
				t.Fatal("secret config retained")
			}
		})
	}
}
func TestInvalidConfigAndPortConflict(t *testing.T) {
	m := helperManager(t)
	c := testRoom()
	c.Secret = "short"
	if err := m.Connect(context.Background(), c); err == nil {
		t.Fatal("invalid secret accepted")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c = testRoom()
	c.Role = "guest"
	c.LocalPort = listener.Addr().(*net.TCPAddr).Port
	if err = m.Connect(context.Background(), c); err == nil || !strings.Contains(err.Error(), "порт") {
		t.Fatalf("occupied port: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err = m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err = m.Connect(ctx, testRoom()); err == nil {
		t.Fatal("connected after shutdown")
	}
}

func nativePaths(t *testing.T) (string, string) {
	t.Helper()
	if os.Getenv("GAMEDOCK_EASYTIER_NATIVE") != "1" {
		t.Skip("set GAMEDOCK_EASYTIER_NATIVE=1 for actual pinned engine tests")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "work", "easytier-dist", "easytier-windows-x86_64"))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "easytier-core.exe"), filepath.Join(root, "easytier-cli.exe")
}
