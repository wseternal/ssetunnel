package agentmod

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// captureLog redirects the standard logger for the duration of a test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return &buf
}

func logLines(buf *bytes.Buffer) []string {
	text := strings.TrimSuffix(buf.String(), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// TestModuleLogWriter_SplitsLines proves one log record per complete line and
// that the byte count reports the full input.
func TestModuleLogWriter_SplitsLines(t *testing.T) {
	buf := captureLog(t)
	w := &moduleLogWriter{moduleID: "com.example.mod", stream: " out"}

	n, err := w.Write([]byte("first\nsecond\n"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != len("first\nsecond\n") {
		t.Errorf("Write returned %d, want %d", n, len("first\nsecond\n"))
	}

	lines := logLines(buf)
	want := []string{
		"module[com.example.mod] out: first",
		"module[com.example.mod] out: second",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d records (%q), want %d", len(lines), lines, len(want))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("record %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

// TestModuleLogWriter_EmitsUnterminatedTail proves output written just before
// the module exits is not lost.
func TestModuleLogWriter_EmitsUnterminatedTail(t *testing.T) {
	buf := captureLog(t)
	w := &moduleLogWriter{moduleID: "mod"}

	if _, err := w.Write([]byte("no newline here")); err != nil {
		t.Fatalf("write: %v", err)
	}

	lines := logLines(buf)
	if len(lines) != 1 || lines[0] != "module[mod]: no newline here" {
		t.Fatalf("got %q, want the stderr-prefixed tail record", lines)
	}
}

// TestModuleLogWriter_TruncatesOversizedLine proves a chunk past the cap is
// truncated rather than buffered whole.
func TestModuleLogWriter_TruncatesOversizedLine(t *testing.T) {
	buf := captureLog(t)
	w := &moduleLogWriter{moduleID: "mod", stream: " out"}

	huge := strings.Repeat("x", maxModuleLogLine+1024)
	n, err := w.Write([]byte(huge))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != len(huge) {
		t.Errorf("Write returned %d, want %d", n, len(huge))
	}

	lines := logLines(buf)
	if len(lines) != 1 {
		t.Fatalf("got %d records, want 1", len(lines))
	}
	payload := strings.TrimPrefix(lines[0], "module[mod] out: ")
	if len(payload) != maxModuleLogLine {
		t.Errorf("truncated payload length = %d, want %d", len(payload), maxModuleLogLine)
	}
}

// TestModuleLogWriter_EmptyWrite stays quiet on a zero-length chunk.
func TestModuleLogWriter_EmptyWrite(t *testing.T) {
	buf := captureLog(t)
	w := &moduleLogWriter{moduleID: "mod"}
	if n, err := w.Write(nil); n != 0 || err != nil {
		t.Errorf("Write(nil) = (%d,%v), want (0,nil)", n, err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no log record, got %q", buf.String())
	}
}
