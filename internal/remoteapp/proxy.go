package remoteapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
)

// ProxyRemoteApp bridges a yamux stream with screen capture and input replay.
// It sends screen dimensions as the first frame, then runs a capture loop
// in one goroutine while reading frames from the stream in the main
// goroutine. A lockedWriter serializes all writes to the stream.
//
// The capture loop takes an initial screenshot on session start and
// subsequent screenshots only when the user triggers a manual refresh
// via the command palette.
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

	// forceCapture signals the capture loop to immediately capture a
	// screenshot. Used by the command palette "Refresh Screenshot" action.
	// Buffered 1; extra signals coalesce.
	forceCapture := make(chan struct{}, 1)

	// streaming toggles the capture loop's continuous streaming mode.
	// Buffered 1; drain-then-send for latest-wins semantics.
	streaming := make(chan bool, 1)

	// maxFPSCh adjusts the capture loop's streaming FPS cap live.
	// Buffered 1; drain-then-send for latest-wins semantics.
	maxFPSCh := make(chan int, 1)

	// lastAckUnixMilli tracks the latest server-ACK'd screenshot timestamp
	// for observability. Loaded at session teardown for the final log line.
	var lastAckUnixMilli atomic.Int64

	var wg sync.WaitGroup
	wg.Add(1)

	// Goroutine: capture screenshots → yamux stream.
	go func() {
		defer wg.Done()
		// fpsCallback emits a FrameFPS event once per second while streaming.
		fpsCallback := func(currentMaxFPS int) {
			if err := lw.writeFPSEvent(currentMaxFPS); err != nil {
				log.Printf("remoteapp: writeFPSEvent: %v", err)
			}
		}
		if err := CaptureLoop(ctx, lw, forceCapture, streaming, maxFPSCh, fpsCallback); err != nil && err != context.Canceled {
			log.Printf("remoteapp: capture loop: %v", err)
			if werr := lw.writeLogEvent("error", fmt.Sprintf("capture loop exited: %v", err)); werr != nil {
				log.Printf("remoteapp: writeLogEvent: %v", werr)
			}
		}
	}()

	// signalForceCapture requests an immediate capture from the capture
	// loop. Non-blocking.
	signalForceCapture := func() {
		select {
		case forceCapture <- struct{}{}:
		default: // already pending; coalesce
		}
	}

	// signalStreaming sends a streaming toggle signal to the capture loop.
	// Non-blocking; drain-then-send for latest-wins semantics.
	signalStreaming := func(on bool) {
		// Drain any pending signal before sending the new one.
		select {
		case <-streaming:
		default:
		}
		select {
		case streaming <- on:
		default:
		}
	}

	// signalMaxFPS sends a max FPS adjustment signal to the capture loop.
	// Non-blocking; drain-then-send for latest-wins semantics.
	signalMaxFPS := func(fps int) {
		select {
		case <-maxFPSCh:
		default:
		}
		select {
		case maxFPSCh <- fps:
		default:
		}
	}

	// Main goroutine: read frames from yamux stream → dispatch.
readLoop:
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

			// Control events: protocol-level actions that do not
			// dispatch to robotgo.
			switch event.Type {
			case "refresh_screenshot":
				log.Printf("remoteapp: refresh_screenshot received")
				signalForceCapture()
				if werr := lw.writeInputAck(InputAck{Type: event.Type, Detail: "refresh"}); werr != nil {
					log.Printf("remoteapp: writeInputAck: %v", werr)
					if errors.Is(werr, ErrWriterClosed) {
						break readLoop
					}
				}
				continue
			case "start_streaming":
				log.Printf("remoteapp: start_streaming received")
				signalStreaming(true)
				if werr := lw.writeInputAck(InputAck{Type: event.Type, Detail: "streaming started"}); werr != nil {
					log.Printf("remoteapp: writeInputAck: %v", werr)
					if errors.Is(werr, ErrWriterClosed) {
						break readLoop
					}
				}
				continue
			case "stop_streaming":
				log.Printf("remoteapp: stop_streaming received")
				signalStreaming(false)
				if werr := lw.writeInputAck(InputAck{Type: event.Type, Detail: "streaming stopped"}); werr != nil {
					log.Printf("remoteapp: writeInputAck: %v", werr)
					if errors.Is(werr, ErrWriterClosed) {
						break readLoop
					}
				}
				continue
			case "set_max_fps":
				fps := event.Amount
				if fps < 1 {
					fps = 1
				}
				if fps > 30 {
					fps = 30
				}
				log.Printf("remoteapp: set_max_fps received: %d", fps)
				signalMaxFPS(fps)
				if werr := lw.writeInputAck(InputAck{Type: event.Type, Detail: strconv.Itoa(fps)}); werr != nil {
					log.Printf("remoteapp: writeInputAck: %v", werr)
					if errors.Is(werr, ErrWriterClosed) {
						break readLoop
					}
				}
				continue
			}

			// Send InputAck for action events (skip mouse_move —
			// no tooltip shown and avoids ~30 acks/sec wire flood).
			if event.Type != "mouse_move" {
				if werr := lw.writeInputAck(InputAck{Type: event.Type, Detail: ackDetail(event)}); werr != nil {
					log.Printf("remoteapp: writeInputAck: %v", werr)
					if errors.Is(werr, ErrWriterClosed) {
						break readLoop // stream is dead, exit read loop
					}
				}
			}

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

// ackDetail builds a brief human-readable detail string for the InputAck.
func ackDetail(event InputEvent) string {
	switch event.Type {
	case "mouse_click":
		return event.Button
	case "mouse_scroll":
		return event.Direction
	case "mouse_drag":
		return event.Button
	case "key_tap":
		if len(event.Modifiers) > 0 {
			return fmt.Sprintf("%s+%s", event.Modifiers[0], event.Key)
		}
		return event.Key
	case "key_toggle":
		return fmt.Sprintf("%s (%s)", event.Key, event.State)
	case "type_text":
		runes := []rune(event.Text)
		if len(runes) > 10 {
			return string(runes[:10]) + "..."
		}
		return event.Text
	case "mouse_move":
		return ""
	case "refresh_screenshot":
		return "refresh"
	case "start_streaming":
		return "streaming started"
	case "stop_streaming":
		return "streaming stopped"
	case "set_max_fps":
		return fmt.Sprintf("fps:%d", event.Amount)
	default:
		return ""
	}
}
