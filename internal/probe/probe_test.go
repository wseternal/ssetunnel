package probe_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wseternal/ssetunnel/internal/probe"
	"github.com/wseternal/ssetunnel/internal/server"
	"github.com/wseternal/ssetunnel/internal/testutil"
)

// probeServerURL runs the real handlers behind a middlebox and returns
// the middlebox URL the probe should target.
func probeServerURL(t *testing.T, cfg testutil.MiddleboxConfig) string {
	t.Helper()
	reg := server.NewRegistry()
	ts := httptest.NewServer(server.NewHandler(reg, time.Hour))
	t.Cleanup(ts.Close)
	mb, err := testutil.StartMiddlebox(ts.URL, cfg)
	if err != nil {
		t.Fatalf("StartMiddlebox: %v", err)
	}
	t.Cleanup(mb.Close)
	return mb.URL
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return s[i+1:]
	}
	return s
}

func TestProbeCliff(t *testing.T) {
	t.Parallel()
	url := probeServerURL(t, testutil.MiddleboxConfig{MaxBody: 64 << 10})
	rep, err := probe.Run(context.Background(), probe.Config{URL: url})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Unsupported {
		t.Fatal("report says unsupported against a real /probe server")
	}
	// 16/32/64 KiB pass (cap is >64 KiB), 128 KiB is the first 413:
	// the cliff lands within one escalation step of the real cap.
	if rep.Cliff != 128<<10 {
		t.Fatalf("Cliff = %d, want %d (one step past the 64 KiB cap)", rep.Cliff, 128<<10)
	}
	for _, m := range rep.Measurements {
		if m.Status != http.StatusOK {
			t.Fatalf("measurement at %d bytes: status %d, want 200", m.Bytes, m.Status)
		}
		if m.RTT <= 0 {
			t.Fatalf("measurement at %d bytes: RTT %v, want > 0", m.Bytes, m.RTT)
		}
	}
	if !strings.Contains(rep.String(), "131072") {
		t.Fatalf("report does not mention the cliff size:\n%s", rep.String())
	}
}

func TestProbePerConnThrottle(t *testing.T) {
	t.Parallel()
	const rate = 1 << 20 // 1 MiB/s per connection
	url := probeServerURL(t, testutil.MiddleboxConfig{PerConnRate: rate})
	rep, err := probe.Run(context.Background(), probe.Config{URL: url, Steps: 3, Parallel: 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.PerConn {
		t.Fatalf("classified aggregate, want per-conn (single %.0f B/s, parallel %.0f B/s)",
			rep.SingleRate, rep.ParallelRate)
	}
	// Wide bound: expected ≈4x, assert ≥2x.
	if rep.ParallelRate < 2*rep.SingleRate {
		t.Fatalf("parallel/single ratio = %.2f, want >= 2 (per-conn throttle)",
			rep.ParallelRate/rep.SingleRate)
	}
	if !strings.Contains(rep.Recommendation, "concurrency") {
		t.Fatalf("recommendation %q does not recommend concurrency", rep.Recommendation)
	}
	if got := lastLine(rep.String()); !strings.HasPrefix(got, "recommendation:") {
		t.Fatalf("report's last line = %q, want it to start with recommendation:", got)
	}
}

func TestProbeGlobalThrottle(t *testing.T) {
	t.Parallel()
	const rate = 1 << 20 // 1 MiB/s shared across all connections
	url := probeServerURL(t, testutil.MiddleboxConfig{GlobalRate: rate})
	rep, err := probe.Run(context.Background(), probe.Config{URL: url, Steps: 3, Parallel: 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.PerConn {
		t.Fatalf("classified per-conn, want aggregate (single %.0f B/s, parallel %.0f B/s)",
			rep.SingleRate, rep.ParallelRate)
	}
	// Wide bound: expected ≈1x, assert <2x.
	if rep.ParallelRate >= 2*rep.SingleRate {
		t.Fatalf("parallel/single ratio = %.2f, want < 2 (aggregate throttle)",
			rep.ParallelRate/rep.SingleRate)
	}
	if !strings.Contains(rep.Recommendation, "concurrency won't help") {
		t.Fatalf("recommendation %q, want aggregate-cap wording", rep.Recommendation)
	}
	if got := lastLine(rep.String()); !strings.HasPrefix(got, "recommendation:") {
		t.Fatalf("report's last line = %q, want it to start with recommendation:", got)
	}
}

func TestProbeUnsupportedServer(t *testing.T) {
	t.Parallel()
	// A server without the /probe endpoint (e.g. a cycle-1 server).
	ts := httptest.NewServer(http.NewServeMux())
	t.Cleanup(ts.Close)
	rep, err := probe.Run(context.Background(), probe.Config{URL: ts.URL})
	if err != nil {
		t.Fatalf("Run: %v (unsupported must be a report, not an error)", err)
	}
	if !rep.Unsupported {
		t.Fatal("report does not say unsupported against a /probe-less server")
	}
	if !strings.Contains(rep.String(), "unsupported") {
		t.Fatalf("report text lacks 'unsupported':\n%s", rep.String())
	}
	if got := lastLine(rep.String()); !strings.HasPrefix(got, "recommendation:") {
		t.Fatalf("report's last line = %q, want it to start with recommendation:", got)
	}
}
