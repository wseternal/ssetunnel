package agentmod

import (
	"bytes"
	"log"
)

// maxModuleLogLine bounds one forwarded line. Module output reaches the agent
// log in chunks; a chunk past this size is truncated rather than buffered.
const maxModuleLogLine = 1 << 20

// moduleLogWriter forwards one module output stream to the agent log, one
// line per record, prefixed with the module id and a tag naming the stream.
// exec.Cmd owns the pipe and its copy goroutine, so the writer never reads
// from a descriptor the module lifecycle can close underneath it. Nothing
// waits for that goroutine on the supervisor path (the supervisor waits on
// *os.Process), which is why no WaitDelay is set.
type moduleLogWriter struct {
	moduleID string
	stream   string // "" for stderr, " out" for stdout
}

// Write logs each complete line and logs an unterminated tail as its own
// record, so output written just before exit is never dropped. A logical line
// larger than the copy buffer therefore appears as several records.
func (w *moduleLogWriter) Write(p []byte) (int, error) {
	written := len(p)
	for len(p) > 0 {
		line := p
		if i := bytes.IndexByte(p, '\n'); i >= 0 {
			line, p = p[:i], p[i+1:]
		} else {
			p = nil
		}
		if len(line) > maxModuleLogLine {
			line = line[:maxModuleLogLine]
		}
		log.Printf("module[%s]%s: %s", w.moduleID, w.stream, line)
	}
	return written, nil
}
