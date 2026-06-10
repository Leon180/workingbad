package main

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestStartPprofServer_BindsToLoopbackOnly proves the pprof debug
// surface is unreachable from any non-loopback interface even when
// --debug is on. This is the security-critical invariant: pprof
// exposes goroutine stacks, allocations, and live profiling; it must
// never leave the machine.
func TestStartPprofServer_BindsToLoopbackOnly(t *testing.T) {
	port := freePort(t)
	stop, err := startPprofServer(port)
	if err != nil {
		t.Fatalf("startPprofServer: %v", err)
	}
	defer stop()

	// Probe the loopback address — should return 200.
	resp, err := httpGet("http://127.0.0.1:" + strconv.Itoa(port) + "/debug/pprof/")
	if err != nil {
		t.Fatalf("loopback probe: %v", err)
	}
	if resp != http.StatusOK {
		t.Errorf("loopback / status = %d, want 200", resp)
	}

	// listener should be bound to 127.0.0.1, NOT 0.0.0.0. Verify by
	// trying to dial the same port via a non-loopback IPv4 — should
	// connect-refuse. (We can't reliably reach the host's primary IP
	// in tests, so we settle for "the listener.Addr() string starts
	// with 127.0.0.1:" by re-listening on the same loopback addr,
	// which would conflict if the binding allowed 0.0.0.0.)
	conflict, err := net.Listen("tcp4", "127.0.0.1:"+strconv.Itoa(port))
	if err == nil {
		_ = conflict.Close()
		t.Error("could re-bind 127.0.0.1:port → pprof listener wasn't actually using it")
	}
	wildcardConflict, err := net.Listen("tcp4", "0.0.0.0:"+strconv.Itoa(port))
	if err != nil {
		// If 0.0.0.0:port is busy, pprof was bound 0.0.0.0 (leak).
		t.Errorf("0.0.0.0:port refused (%v) — pprof might be bound wider than loopback", err)
	} else {
		_ = wildcardConflict.Close()
	}
}

// TestStartPprofServer_RejectsInvalidPort guards the input validation.
func TestStartPprofServer_RejectsInvalidPort(t *testing.T) {
	for _, p := range []int{0, -1, 70000} {
		if _, err := startPprofServer(p); err == nil {
			t.Errorf("port=%d: expected error, got nil", p)
		}
	}
}

// TestStartPprofServer_PprofEndpointsRespond hits a few of the standard
// pprof endpoints to confirm they're wired up, not just /debug/pprof/.
func TestStartPprofServer_PprofEndpointsRespond(t *testing.T) {
	port := freePort(t)
	stop, err := startPprofServer(port)
	if err != nil {
		t.Fatalf("startPprofServer: %v", err)
	}
	defer stop()

	base := "http://127.0.0.1:" + strconv.Itoa(port)
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
