package main

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestStartPprofServer_BindsToLoopbackOnly proves the pprof debug
// surface is bound to 127.0.0.1 only. This is the security-critical
// invariant: pprof exposes goroutine stacks, allocations, and live
// profiling; it must never leave the machine.
//
// The original "try to bind 0.0.0.0:port and expect success" approach
// fails on Linux, where INADDR_ANY overlaps the explicit 127.0.0.1
// bind (different from macOS's looser semantics). We instead read the
// listener's actual bound address — the Go stdlib can't lie about
// what it bound to.
func TestStartPprofServer_BindsToLoopbackOnly(t *testing.T) {
	port := freePort(t)
	addr, stop, err := startPprofServer(port)
	if err != nil {
		t.Fatalf("startPprofServer: %v", err)
	}
	defer stop()

	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Errorf("bound addr = %q, want prefix 127.0.0.1: (loopback-only)", addr)
	}

	// Sanity: the loopback endpoint actually answers.
	code, err := httpGet("http://" + addr + "/debug/pprof/")
	if err != nil {
		t.Fatalf("loopback probe: %v", err)
	}
	if code != http.StatusOK {
		t.Errorf("loopback / status = %d, want 200", code)
	}
}

// TestStartPprofServer_RejectsInvalidPort guards the input validation.
func TestStartPprofServer_RejectsInvalidPort(t *testing.T) {
	for _, p := range []int{0, -1, 70000} {
		if _, _, err := startPprofServer(p); err == nil {
			t.Errorf("port=%d: expected error, got nil", p)
		}
	}
}

// TestStartPprofServer_PprofEndpointsRespond hits a few of the standard
// pprof endpoints to confirm they're wired up, not just /debug/pprof/.
func TestStartPprofServer_PprofEndpointsRespond(t *testing.T) {
	port := freePort(t)
	addr, stop, err := startPprofServer(port)
	if err != nil {
		t.Fatalf("startPprofServer: %v", err)
	}
	defer stop()

	base := "http://" + addr
	for _, path := range []string{
		"/debug/pprof/",
		"/debug/pprof/cmdline",
		"/debug/pprof/symbol",
		"/debug/pprof/heap?debug=1",
	} {
		t.Run(strings.TrimPrefix(path, "/debug/pprof/"), func(t *testing.T) {
			code, err := httpGet(base + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			if code != http.StatusOK {
				t.Errorf("GET %s: status = %d, want 200", path, code)
			}
		})
	}
}

// freePort asks the kernel for a fresh ephemeral loopback port and
// returns it. The port is closed before return so the caller can bind
// it — there's a tiny window where another process could steal it,
// but for fast in-package tests this is fine.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func httpGet(url string) (int, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, nil
}
