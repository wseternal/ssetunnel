// Package probe measures a live tunnel server's POST path (cycle-2 plan
// step 6): the body-size cliff (escalating sizes), RTT vs body size, and
// per-connection vs aggregate throttling (1 vs N parallel streams). It
// reports plain text ending in a recommendation for --batch-size and
// --concurrency. Probing goes through POST /probe, never /events — an
// /events probe would hijack the live agent's session (plan decision 6).
package probe

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Default knobs (struct fields per spec style; zero values pick these).
const (
	defaultSteps    = 7        // 16 KiB .. 1 MiB doublings
	defaultParallel = 4        // phase-3 stream count
	defaultMaxBody  = 2 << 20  // matches the server's /probe cap
	startSize       = 16 << 10 // first escalation size
)

// Config configures Run.
type Config struct {
	URL      string       // tunnel server base URL, e.g. http://host:port
	Client   *http.Client // nil → http.DefaultClient
	Steps    int          // escalation steps from 16 KiB; 0 → 7
	Parallel int          // phase-3 parallel stream count; 0 → 4
	MaxBody  int          // escalation ceiling; 0 → 2 MiB
}

// Measurement is one probed POST: size, outcome, round-trip time.
type Measurement struct {
	Bytes  int
	Status int
	RTT    time.Duration
}

// Report is the outcome of a probe run.
type Report struct {
	URL            string
	Unsupported    bool          // server has no /probe endpoint
	Measurements   []Measurement // size ladder, phases 1+2
	Cliff          int           // first rejected size; 0 = none within MaxBody
	ProbeSize      int           // size used for the throughput comparison
	SingleRate     float64       // bytes/sec, 1 stream
	ParallelRate   float64       // bytes/sec, Parallel streams aggregate
	PerConn        bool          // true = per-connection throttle (parallel scales)
	Recommendation string
}

// Run executes the three probe phases against cfg.URL.
func Run(ctx context.Context, cfg Config) (Report, error) {
	if cfg.URL == "" {
		return Report{}, fmt.Errorf("probe: Config.URL is required")
	}
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	steps := cfg.Steps
	if steps <= 0 {
		steps = defaultSteps
	}
	parallel := cfg.Parallel
	if parallel <= 0 {
		parallel = defaultParallel
	}
	maxBody := cfg.MaxBody
	if maxBody <= 0 {
		maxBody = defaultMaxBody
	}

	rep := Report{URL: cfg.URL}
	post := func(size int) (Measurement, error) {
		return timedPost(ctx, client, cfg.URL, size)
	}

	// Phases 1+2: escalating sizes → body-size cliff + RTT-vs-size table.
	for size, i := startSize, 0; i < steps && size <= maxBody; size, i = size*2, i+1 {
		m, err := post(size)
		if err != nil {
			return Report{}, err
		}
		if m.Status == http.StatusNotFound && len(rep.Measurements) == 0 {
			// No /probe endpoint: a clean "unsupported" report, not an error.
			rep.Unsupported = true
			rep.Recommendation = "recommendation: server does not support probing (no /probe endpoint)"
			return rep, nil
		}
		if m.Status != http.StatusOK {
			rep.Cliff = size
			break
		}
		rep.Measurements = append(rep.Measurements, m)
	}

	if len(rep.Measurements) == 0 {
		// Even the smallest size failed (and it was not a 404).
		rep.Recommendation = fmt.Sprintf(
			"recommendation: server rejects POST bodies of %d bytes — batch cap extremely tight", startSize)
		return rep, nil
	}

	// Phase 3: 1 vs N parallel fixed-size POSTs → throttle classification.
	rep.ProbeSize = rep.Measurements[len(rep.Measurements)-1].Bytes
	single, err := post(rep.ProbeSize)
	if err != nil {
		return Report{}, err
	}
	rep.SingleRate = float64(rep.ProbeSize) / single.RTT.Seconds()

	start := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, parallel)
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := post(rep.ProbeSize); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return Report{}, err
		}
	}
	rep.ParallelRate = float64(parallel*rep.ProbeSize) / time.Since(start).Seconds()

	// Parallel scales ≈N× under a per-connection throttle, ≈1× under an
	// aggregate cap. The N/2 boundary keeps the margin wide.
	rep.PerConn = rep.ParallelRate >= float64(parallel)/2*rep.SingleRate
	batch := rep.ProbeSize
	if rep.PerConn {
		rep.Recommendation = fmt.Sprintf(
			"recommendation: --batch-size %d --concurrency %d (per-connection throttle)", batch, parallel)
	} else {
		rep.Recommendation = fmt.Sprintf(
			"recommendation: --batch-size %d (aggregate cap — concurrency won't help)", batch)
	}
	return rep, nil
}

// timedPost issues one POST /probe of size bytes and measures its RTT.
func timedPost(ctx context.Context, client *http.Client, baseURL string, size int) (Measurement, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		baseURL+"/probe", bytes.NewReader(make([]byte, size)))
	if err != nil {
		return Measurement{}, fmt.Errorf("build probe request: %w", err)
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return Measurement{}, fmt.Errorf("probe POST %d bytes: %w", size, err)
	}
	io.Copy(io.Discard, resp.Body) // always drain
	resp.Body.Close()
	return Measurement{Bytes: size, Status: resp.StatusCode, RTT: time.Since(start)}, nil
}

// String renders the plain-text report; the last line is always the
// recommendation.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "probe report for %s\n", r.URL)
	if r.Unsupported {
		b.WriteString("POST /probe: unsupported (404 — server predates the probe endpoint)\n")
		b.WriteString(r.Recommendation + "\n")
		return b.String()
	}
	b.WriteString("size ladder:\n")
	for _, m := range r.Measurements {
		fmt.Fprintf(&b, "  %8d bytes  %d  %v\n", m.Bytes, m.Status, m.RTT)
	}
	if r.Cliff > 0 {
		fmt.Fprintf(&b, "body-size cliff: %d bytes rejected\n", r.Cliff)
	} else {
		b.WriteString("body-size cliff: none within the probed range\n")
	}
	if r.ProbeSize > 0 {
		fmt.Fprintf(&b, "throughput at %d bytes: 1 stream %.1f MB/s, parallel %.1f MB/s → %s\n",
			r.ProbeSize, r.SingleRate/(1<<20), r.ParallelRate/(1<<20),
			map[bool]string{true: "per-connection throttle", false: "aggregate throttle"}[r.PerConn])
	}
	b.WriteString(r.Recommendation + "\n")
	return b.String()
}
