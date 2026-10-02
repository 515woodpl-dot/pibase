package gateway

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
)

func TestM5bRealtimePiMemory(t *testing.T) {
	if os.Getenv("PIBASE_MEASURE_REALTIME") != "true" {
		t.Skip("PIBASE_MEASURE_REALTIME=true is required")
	}
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Fatal("PIBASE_TEST_ADMIN_DATABASE_URL is required")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required for the external socket process")
	}
	t.Setenv("PIBASE_SCHEDULER_DISABLED", "true")
	t.Setenv("PIBASE_REALTIME_MAX_CONNECTIONS", "32")
	ctx := context.Background()
	const password = "integration-auth-password"
	masterKey := bytes.Repeat([]byte{8}, 32)
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	project, err := control.CreateProject(ctx, adminURL, "gateway-m5b-memory", masterKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupGatewayProject(t, adminURL, project)
	authURL := databaseURL(t, adminURL, "_control", "authenticator", password)
	server, err := New(ctx, authURL, masterKey, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	runtime.GC()
	baseline := processPSSKiB(t)
	script, err := filepath.Abs("../../tests/realtime-memory.mjs")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(node, "--experimental-websocket", script)
	command.Env = append(os.Environ(), "PIBASE_TEST_URL="+httpServer.URL, "PIBASE_TEST_ANON_KEY="+project.AnonKey, "PIBASE_REALTIME_CONNECTIONS=32")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	ready := false
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "READY 32" {
			ready = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !ready {
		t.Fatal("external socket process exited before READY")
	}
	time.Sleep(time.Second)
	runtime.GC()
	loaded := processPSSKiB(t)
	delta := loaded - baseline
	t.Logf("PIBASE_REALTIME_MEMORY model=%q arch=%s connections=32 baseline_pss_kib=%d loaded_pss_kib=%d delta_pss_kib=%d delta_bytes_per_socket=%d", piModel(), runtime.GOARCH, baseline, loaded, delta, delta*1024/32)
}

func processPSSKiB(t *testing.T) int64 {
	t.Helper()
	content, err := os.ReadFile("/proc/self/smaps_rollup")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "Pss:" {
			value, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return value
		}
	}
	t.Fatal("Pss not found in /proc/self/smaps_rollup")
	return 0
}

func piModel() string {
	content, err := os.ReadFile("/proc/device-tree/model")
	if err != nil {
		return fmt.Sprintf("unknown: %v", err)
	}
	return strings.TrimRight(string(content), "\x00")
}
