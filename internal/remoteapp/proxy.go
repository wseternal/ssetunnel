package remoteapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
)

// defaultFPS is the default screenshot rate.
const defaultFPS = 3

// ProxyRemoteApp bridges a yamux stream with screen capture and input replay.
// It sends screen dimensions as the first frame, then runs a capture loop
// in one goroutine while reading input events from the stream in the main
// goroutine. When either side closes, both are torn down.
func ProxyRemoteApp(stream net.Conn) {
	if !Enabled() {
		log.Printf("remoteapp: not supported on this OS")
		stream.Close()
		return
	}

	screenW, screenH := GetScreenSize()
	log.Printf("remoteapp: session started (screen=%dx%d)", screenW, screenH)

	// Send screen dimensions as the first frame so the frontend can
	// scale input coordinates.
	info, err := json.Marshal(ScreenInfo{Width: screenW, Height: screenH})
	if err != nil {
		log.Printf("remoteapp: marshal screen info: %v", err)
		stream.Close()
		return
	}
	if err := WriteFrame(stream, FrameScreenInfo, info); err != nil {
		log.Printf("remoteapp: write screen info: %v", err)
		stream.Close()
		return
	}

	// Emit observability event: session started.
	_ = WriteLogEvent(stream, "info", fmt.Sprintf("session started (screen=%dx%d)", screenW, screenH))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(1)

	// Goroutine: capture screenshots → yamux stream.
	go func() {
		defer wg.Done()
		if err := CaptureLoop(ctx, stream, defaultFPS); err != nil && err != context.Canceled {
			log.Printf("remoteapp: capture loop: %v", err)
			_ = WriteLogEvent(stream, "error", fmt.Sprintf("capture loop exited: %v", err))
		}
	}()

	// Main goroutine: read input events from yamux stream → dispatch.
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
				_ = WriteLogEvent(stream, "warn", fmt.Sprintf("bad input JSON: %v", err))
				continue
			}
			if err := DispatchInput(event, screenW, screenH); err != nil {
				log.Printf("remoteapp: dispatch input: %v", err)
				_ = WriteLogEvent(stream, "warn", fmt.Sprintf("input dispatch failed: %v", err))
			} else {
				_ = WriteLogEvent(stream, "info", fmt.Sprintf("input dispatched: %s", event.Type))
			}
		default:
			log.Printf("remoteapp: unexpected frame type: 0x%02x", frameType)
			_ = WriteLogEvent(stream, "warn", fmt.Sprintf("unexpected frame type: 0x%02x", frameType))
		}
	}

	// Stream closed: cancel capture loop and wait.
	cancel()
	ReleaseAllInputs() // Release any held keys/buttons from lost "up" events.
	stream.Close()
	wg.Wait()
	log.Printf("remoteapp: session ended")
	_ = WriteLogEvent(stream, "info", "session ended")
}
