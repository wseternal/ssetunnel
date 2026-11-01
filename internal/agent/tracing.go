//go:build !windows

package agent

import (
	"fmt"
	"io"
	"log"
	"sync/atomic"
	"time"
)

// tracedReader wraps an io.Reader to count bytes read and log periodic
// summaries. Used only when verbose tracing is enabled.
type tracedReader struct {
	r       io.Reader
	label   string
	n       atomic.Int64
	lastLog atomic.Int64 // UnixNano of last summary
}

// tracedWriter wraps an io.Writer to count bytes written and log
// periodic summaries. Used only when verbose tracing is enabled.
type tracedWriter struct {
	w       io.Writer
	label   string
	n       atomic.Int64
	lastLog atomic.Int64 // UnixNano of last summary
}

// traceSummaryInterval controls how often periodic byte-count summaries
// are logged during bulk transfers. Short enough to catch stalls, long
// enough to avoid flooding.
const traceSummaryInterval = 10 * int64(time.Second)

func (t *tracedReader) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	t.n.Add(int64(n))
	t.maybeLogSummary()
	return n, err
}

func (t *tracedWriter) Write(p []byte) (int, error) {
	n, err := t.w.Write(p)
	t.n.Add(int64(n))
	t.maybeLogSummary()
	return n, err
}

func (t *tracedReader) maybeLogSummary() {
	now := time.Now().UnixNano()
	last := t.lastLog.Load()
	if now-last >= traceSummaryInterval {
		if t.lastLog.CompareAndSwap(last, now) {
			log.Printf("agent: [verbose] %s: %d bytes read", t.label, t.n.Load())
		}
	}
}

func (t *tracedWriter) maybeLogSummary() {
	now := time.Now().UnixNano()
	last := t.lastLog.Load()
	if now-last >= traceSummaryInterval {
		if t.lastLog.CompareAndSwap(last, now) {
			log.Printf("agent: [verbose] %s: %d bytes written", t.label, t.n.Load())
		}
	}
}

// tracedCopy is like io.Copy but logs start, periodic summaries, and
// completion with byte counts and elapsed time. The label identifies
// this copy direction in log output.
func tracedCopy(label string, dst io.Writer, src io.Reader) (int64, error) {
	log.Printf("agent: [verbose] %s: copy started", label)
	start := time.Now()

	tr := &tracedReader{r: src, label: label + " (read)"}
	tw := &tracedWriter{w: dst, label: label + " (write)"}
	now := start.UnixNano()
	tr.lastLog.Store(now)
	tw.lastLog.Store(now)

	n, err := io.Copy(tw, tr)

	elapsed := time.Since(start)
	rate := float64(n) / elapsed.Seconds()
	if err != nil {
		log.Printf("agent: [verbose] %s: copy done (%s, %d bytes, %.1f KB/s): %v",
			label, elapsed.Round(time.Millisecond), n, rate/1024, err)
	} else {
		log.Printf("agent: [verbose] %s: copy done (%s, %d bytes, %.1f KB/s)",
			label, elapsed.Round(time.Millisecond), n, rate/1024)
	}
	return n, err
}

// verboseTrace logs a message when verbose tracing is enabled.
// Callers should guard with agent.Verbose to avoid log noise.
func verboseTrace(format string, args ...interface{}) {
	log.Printf("agent: [verbose] "+format, args...)
}

// formatExitStatus returns a human-readable description of a process
// exit status from cmd.Wait()'s error.
func formatExitStatus(err error) string {
	if err == nil {
		return "exit 0"
	}
	return fmt.Sprintf("exit error: %v", err)
}
