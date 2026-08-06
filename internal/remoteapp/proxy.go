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
// The capture loop is signal-driven: it captures immediately when an
// "action" input event (click, key tap, scroll, etc.) arrives via the
// captureNow channel, and falls back to a 15-second idle timer when no
// actions are received. Mouse-move events do NOT trigger captures.
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

	// captureNow signals the capture loop to take an immediate screenshot.
	// Buffered 1 so the proxy never blocks on send; extra signals are
	// coalesced (the capture loop drains on each receive).
	captureNow := make(chan struct{}, 1)

	// lastAckUnixMilli tracks the latest server-ACK'd screenshot timestamp
	// for observability. Loaded at session teardown for the final log line.
	var lastAckUnixMilli atomic.Int64

	var wg sync.WaitGroup
	wg.Add(1)

	// Goroutine: capture screenshots → yamux stream.
	go func() {
		defer wg.Done()
		if err := CaptureLoop(ctx, lw, captureNow); err != nil && err != context.Canceled {
			log.Printf("remoteapp: capture loop: %v", err)
			if werr := lw.writeLogEvent("error", fmt.Sprintf("capture loop exited: %v", err)); werr != nil {
				log.Printf("remoteapp: writeLogEvent: %v", werr)
			}
		}
	}()

	// signalCapture sends a non-blocking signal to the capture loop.
	signalCapture := func() {
		select {
		case captureNow <- struct{}{}:
		default: // already pending; coalesce
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
			if err := DispatchInput(event, screenW, screenH); err != nil {
				log.Printf("remoteapp: dispatch input: %v", err)
				if werr := lw.writeLogEvent("warn", fmt.Sprintf("input dispatch failed: %v", err)); werr != nil {
					log.Printf("remoteapp: writeLogEvent: %v", werr)
				}
			} else {
				// Signal capture on action events (everything except mouse_move).
				if event.Type != "mouse_move" {
					signalCapture()
					if werr := lw.writeLogEvent("info", "input dispatched: "+event.Type); werr != nil {
						log.Printf("remoteapp: writeLogEvent: %v", werr)
					}
				}
				// mouse_move: dispatch silently, no capture signal.
			}
		case FrameScreenshotAck:
			if ts, ok := ParseScreenshotAck(data); ok {
				lastAckUnixMilli.Store(ts.UnixMilli())
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
