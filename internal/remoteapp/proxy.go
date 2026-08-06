package remoteapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
)

// ProxyRemoteApp bridges a yamux stream with screen capture and input replay.
// It sends screen dimensions as the first frame, then runs a capture loop
// in one goroutine while reading frames from the stream in the main
// goroutine. A lockedWriter serializes all writes to the stream.
//
// The capture loop uses deferred capture: every input event signals the
// inputReceived channel, resetting a 3-second deferral timer. A screenshot
// is taken only after the input stream has been quiet for 3 seconds. This
// avoids uploading screenshots that will be immediately stale.
//
// For every input event received, the proxy sends a FrameInputAck back to
// the server so the console UI can display live feedback tooltips.
//
// The server sends FrameScreenshotAck frames to acknowledge receipt of
// screenshots. The proxy tracks the latest ACK timestamp for observability.
func ProxyRemoteApp(stream net.Conn) {
	if !Enabled() {
		log.Printf("remoteapp: not supported on this OS")
		stream.Close()
		return
	}

	screenW, screenH := GetScreenSize()
	log.Printf("remoteapp: session started (screen=%dx%d)", screenW, screenH)

	// Wrap stream in a lockedWriter for concurrent-safe frame writes.
	lw := &lockedWriter{w: stream}

	// Send screen dimensions as the first frame so the frontend can
	// scale input coordinates.
	info, err := json.Marshal(ScreenInfo{Width: screenW, Height: screenH})
	if err != nil {
		log.Printf("remoteapp: marshal screen info: %v", err)
		stream.Close()
		return
	}
	if err := lw.writeFrame(FrameScreenInfo, info); err != nil {
		log.Printf("remoteapp: write screen info: %v", err)
		stream.Close()
		return
	}

	// Emit observability event: session started.
	if err := lw.writeLogEvent("info", fmt.Sprintf("session started (screen=%dx%d)", screenW, screenH)); err != nil {
		log.Printf("remoteapp: writeLogEvent: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// inputReceived signals the capture loop that an input event arrived,
	// resetting its 3-second deferral timer. Buffered 1 so the proxy never
	// blocks on send; extra signals are coalesced.
	inputReceived := make(chan struct{}, 1)

	// lastAckUnixMilli tracks the latest server-ACK'd screenshot timestamp
	// for observability. Loaded at session teardown for the final log line.
	var lastAckUnixMilli atomic.Int64

	var wg sync.WaitGroup
	wg.Add(1)

	// Goroutine: capture screenshots → yamux stream.
	go func() {
		defer wg.Done()
		if err := CaptureLoop(ctx, lw, inputReceived); err != nil && err != context.Canceled {
			log.Printf("remoteapp: capture loop: %v", err)
			if werr := lw.writeLogEvent("error", fmt.Sprintf("capture loop exited: %v", err)); werr != nil {
				log.Printf("remoteapp: writeLogEvent: %v", werr)
			}
		}
	}()

	// signalInput notifies the capture loop that an input event arrived,
	// resetting its deferral timer. Non-blocking; extra signals coalesce.
	signalInput := func() {
		select {
		case inputReceived <- struct{}{}:
		default: // already pending; coalesce
		}
	}

	// ackDetail builds a brief human-readable detail string for the InputAck.
	ackDetail := func(event InputEvent) string {
		switch event.Type {
		case "mouse_click":
			return event.Button
		case "mouse_scroll":
			return event.Direction
		case "key_tap":
			if len(event.Modifiers) > 0 {
				return fmt.Sprintf("%s+%s", event.Modifiers[0], event.Key)
			}
			return event.Key
		case "type_text":
			if len(event.Text) > 10 {
				return event.Text[:10] + "..."
			}
			return event.Text
		case "mouse_move":
			return ""
		default:
			return ""
		}
	}

	// Main goroutine: read frames from yamux stream → dispatch.
	for {
		frameType, data, err := ReadFrame(stream)
		if err != nil {
			break // stream closed
		}
		switch frameType {
		case FrameInput:
			var event InputEvent
			if err := json.Unmarshal(data, &event); err != nil {
				log.Printf("remoteapp: bad input JSON: %v", err)
				if werr := lw.writeLogEvent("warn", fmt.Sprintf("bad input JSON: %v", err)); werr != nil {
					log.Printf("remoteapp: writeLogEvent: %v", werr)
				}
				continue
			}

			// Send InputAck for every input event so the console UI
			// can display live feedback tooltips on the screenshot.
			if werr := lw.writeInputAck(InputAck{Type: event.Type, Detail: ackDetail(event)}); werr != nil {
				log.Printf("remoteapp: writeInputAck: %v", werr)
			}

			// Signal deferred capture: every input resets the 3s timer.
			signalInput()

			if err := DispatchInput(event, screenW, screenH); err != nil {
				log.Printf("remoteapp: dispatch input: %v", err)
				if werr := lw.writeLogEvent("warn", fmt.Sprintf("input dispatch failed: %v", err)); werr != nil {
					log.Printf("remoteapp: writeLogEvent: %v", werr)
				}
			}
		case FrameScreenshotAck:
			if ts, ok := ParseScreenshotAck(data); ok {
				lastAckUnixMilli.Store(ts.UnixMilli())
			} else {
				log.Printf("remoteapp: malformed screenshot ACK (%d bytes)", len(data))
				if werr := lw.writeLogEvent("warn", fmt.Sprintf("malformed screenshot ACK (%d bytes)", len(data))); werr != nil {
					log.Printf("remoteapp: writeLogEvent: %v", werr)
				}
			}
		default:
			log.Printf("remoteapp: unexpected frame type: 0x%02x", frameType)
			if werr := lw.writeLogEvent("warn", fmt.Sprintf("unexpected frame type: 0x%02x", frameType)); werr != nil {
				log.Printf("remoteapp: writeLogEvent: %v", werr)
			}
		}
	}

	// Stream closed: cancel capture loop and wait for it to fully exit.
	cancel()
	ReleaseAllInputs() // Release any held keys/buttons from lost "up" events.
	wg.Wait()
	log.Printf("remoteapp: session ended (lastAck=%d)", lastAckUnixMilli.Load())
	if err := lw.writeLogEvent("info", "session ended"); err != nil {
		log.Printf("remoteapp: writeLogEvent: %v", err)
	}
	lw.close()
	stream.Close()
}
