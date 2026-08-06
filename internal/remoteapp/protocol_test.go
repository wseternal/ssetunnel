package remoteapp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestWriteReadFrameRoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		frameType byte
		data      []byte
	}{
		{"screenshot", FrameScreenshot, []byte("fake-jpeg-data")},
		{"input", FrameInput, []byte(`{"type":"mouse_click","x":100,"y":200}`)},
		{"screeninfo", FrameScreenInfo, []byte(`{"width":1920,"height":1080}`)},
		{"empty data", FrameScreenshot, nil},
		{"large data", FrameScreenshot, bytes.Repeat([]byte{0xAB}, 1<<16)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := WriteFrame(&buf, tt.frameType, tt.data)
			if err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}

			gotType, gotData, err := ReadFrame(&buf)
			if err != nil {
				t.Fatalf("ReadFrame: %v", err)
			}
			if gotType != tt.frameType {
				t.Errorf("frame type: got 0x%02x, want 0x%02x", gotType, tt.frameType)
			}
			if tt.data == nil {
				if gotData != nil {
					t.Errorf("data: got %d bytes, want nil", len(gotData))
				}
			} else if !bytes.Equal(gotData, tt.data) {
				t.Errorf("data: got %d bytes, want %d bytes", len(gotData), len(tt.data))
			}
		})
	}
}

func TestWriteFrameTooLarge(t *testing.T) {
	var buf bytes.Buffer
	data := bytes.Repeat([]byte{0}, maxFrameSize+1)
	err := WriteFrame(&buf, FrameScreenshot, data)
	if err == nil {
		t.Fatal("expected error for oversized frame")
	}
}

func TestReadFrameTooLarge(t *testing.T) {
	// Manually write a frame with length > maxFrameSize.
	var buf bytes.Buffer
	header := [5]byte{FrameScreenshot}
	// Set length to maxFrameSize + 1.
	l := uint32(maxFrameSize + 1)
	header[1] = byte(l >> 24)
	header[2] = byte(l >> 16)
	header[3] = byte(l >> 8)
	header[4] = byte(l)
	buf.Write(header[:])

	_, _, err := ReadFrame(&buf)
	if err == nil {
		t.Fatal("expected error for oversized frame")
	}
}

func TestMultipleFramesSequential(t *testing.T) {
	var buf bytes.Buffer
	frames := []struct {
		ft   byte
		data []byte
	}{
		{FrameScreenshot, []byte("img1")},
		{FrameInput, []byte("evt1")},
		{FrameScreenshot, []byte("img2")},
	}
	for _, f := range frames {
		if err := WriteFrame(&buf, f.ft, f.data); err != nil {
			t.Fatalf("WriteFrame: %v", err)
		}
	}
	for _, want := range frames {
		gotType, gotData, err := ReadFrame(&buf)
		if err != nil {
			t.Fatalf("ReadFrame: %v", err)
		}
		if gotType != want.ft || !bytes.Equal(gotData, want.data) {
			t.Errorf("got (0x%02x, %q), want (0x%02x, %q)", gotType, gotData, want.ft, want.data)
		}
	}
}

func TestReadFrameIntoRoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		frameType byte
		data      []byte
	}{
		{"screenshot", FrameScreenshot, []byte("fake-jpeg-data")},
		{"input", FrameInput, []byte(`{"type":"mouse_click","x":100,"y":200}`)},
		{"empty data", FrameScreenshot, nil},
		{"large data", FrameScreenshot, bytes.Repeat([]byte{0xAB}, 1<<16)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := WriteFrame(&buf, tt.frameType, tt.data)
			if err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}

			readBuf := make([]byte, maxFrameSize)
			gotType, n, err := ReadFrameInto(&buf, readBuf)
			if err != nil {
				t.Fatalf("ReadFrameInto: %v", err)
			}
			if gotType != tt.frameType {
				t.Errorf("frame type: got 0x%02x, want 0x%02x", gotType, tt.frameType)
			}
			wantLen := 0
			if tt.data != nil {
				wantLen = len(tt.data)
			}
			if n != wantLen {
				t.Errorf("n: got %d, want %d", n, wantLen)
			}
			if tt.data != nil && !bytes.Equal(readBuf[:n], tt.data) {
				t.Errorf("data mismatch")
			}
		})
	}
}

func TestReadFrameIntoBufferTooSmall(t *testing.T) {
	var buf bytes.Buffer
	data := bytes.Repeat([]byte{0xAB}, 1024)
	if err := WriteFrame(&buf, FrameScreenshot, data); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	// Provide a buffer smaller than the frame payload.
	small := make([]byte, 256)
	_, _, err := ReadFrameInto(&buf, small)
	if err == nil {
		t.Fatal("expected error for buffer too small")
	}
}

func BenchmarkWriteFrame(b *testing.B) {
	data := bytes.Repeat([]byte{0xAB}, 150_000) // ~150 KB JPEG
	var buf bytes.Buffer
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		_ = WriteFrame(&buf, FrameScreenshot, data)
	}
}

func BenchmarkReadFrameInto(b *testing.B) {
	data := bytes.Repeat([]byte{0xAB}, 150_000)
	readBuf := make([]byte, maxFrameSize)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		_ = WriteFrame(&buf, FrameScreenshot, data)
		_, _, _ = ReadFrameInto(&buf, readBuf)
	}
}

func TestInputEventJSON(t *testing.T) {
	raw := `{"type":"mouse_click","x":500,"y":300,"button":"left"}`
	var evt InputEvent
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Type != "mouse_click" || evt.X != 500 || evt.Y != 300 || evt.Button != "left" {
		t.Errorf("unexpected event: %+v", evt)
	}
}

func TestWriteLogEventRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteLogEvent(&buf, "warn", "capture failed"); err != nil {
		t.Fatalf("WriteLogEvent: %v", err)
	}

	frameType, data, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if frameType != FrameLogEvent {
		t.Errorf("frame type: got 0x%02x, want 0x%02x", frameType, FrameLogEvent)
	}

	var evt LogEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if evt.Severity != "warn" {
		t.Errorf("severity: got %q, want %q", evt.Severity, "warn")
	}
	if evt.Source != "agent" {
		t.Errorf("source: got %q, want %q", evt.Source, "agent")
	}
	if evt.Message != "capture failed" {
		t.Errorf("message: got %q, want %q", evt.Message, "capture failed")
	}
	if evt.TS == "" {
		t.Error("timestamp should not be empty")
	}
}

func TestLockedWriterConcurrent(t *testing.T) {
	const goroutines = 10
	const writesPerGoroutine = 100

	var buf bytes.Buffer
	lw := &lockedWriter{w: &buf}

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < writesPerGoroutine; i++ {
				msg := fmt.Sprintf("g%d-msg%d", id, i)
				if err := lw.writeLogEvent("info", msg); err != nil {
					t.Errorf("writeLogEvent: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	lw.close()

	// Verify all frames are parseable.
	reader := bytes.NewReader(buf.Bytes())
	count := 0
	for {
		frameType, data, err := ReadFrame(reader)
		if err != nil {
			break
		}
		if frameType != FrameLogEvent {
			t.Errorf("unexpected frame type: 0x%02x", frameType)
		}
		var evt LogEvent
		if err := json.Unmarshal(data, &evt); err != nil {
			t.Fatalf("unmarshal log event at frame %d: %v", count, err)
		}
		if evt.Source != "agent" {
			t.Errorf("frame %d: source = %q, want %q", count, evt.Source, "agent")
		}
		count++
	}

	want := goroutines * writesPerGoroutine
	if count != want {
		t.Errorf("frame count: got %d, want %d", count, want)
	}
}

func TestLockedWriterClosePreventsWrites(t *testing.T) {
	var buf bytes.Buffer
	lw := &lockedWriter{w: &buf}
	lw.close()

	if err := lw.writeLogEvent("info", "should fail"); err != ErrWriterClosed {
		t.Errorf("writeLogEvent after close: got %v, want %v", err, ErrWriterClosed)
	}
	if err := lw.writeFrame(FrameScreenshot, []byte("data")); err != ErrWriterClosed {
		t.Errorf("writeFrame after close: got %v, want %v", err, ErrWriterClosed)
	}
	_, err := lw.Write([]byte("raw"))
	if err != ErrWriterClosed {
		t.Errorf("Write after close: got %v, want %v", err, ErrWriterClosed)
	}
}

func TestScreenshotTimestampRoundTrip(t *testing.T) {
	jpegData := []byte("fake-jpeg-data-for-timestamp-test")
	ts := time.Date(2025, 7, 31, 12, 30, 45, 123_000_000, time.UTC)

	var buf bytes.Buffer
	if err := WriteScreenshotWithTimestamp(&buf, jpegData, ts); err != nil {
		t.Fatalf("WriteScreenshotWithTimestamp: %v", err)
	}

	frameType, data, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if frameType != FrameScreenshot {
		t.Errorf("frame type: got 0x%02x, want 0x%02x", frameType, FrameScreenshot)
	}

	gotTS, gotJPEG, ok := ParseScreenshotTimestamp(data)
	if !ok {
		t.Fatal("ParseScreenshotTimestamp: ok=false")
	}
	if !gotTS.Equal(ts.Truncate(time.Millisecond)) {
		t.Errorf("timestamp: got %v, want %v", gotTS, ts)
	}
	if !bytes.Equal(gotJPEG, jpegData) {
		t.Errorf("jpeg data: got %d bytes, want %d bytes", len(gotJPEG), len(jpegData))
	}
}

func TestParseScreenshotTimestampTooShort(t *testing.T) {
	_, _, ok := ParseScreenshotTimestamp([]byte("short"))
	if ok {
		t.Error("expected ok=false for short payload")
	}
}

func TestScreenshotAckRoundTrip(t *testing.T) {
	ts := time.Date(2025, 7, 31, 15, 0, 0, 0, time.UTC)

	var buf bytes.Buffer
	if err := WriteScreenshotAck(&buf, ts); err != nil {
		t.Fatalf("WriteScreenshotAck: %v", err)
	}

	frameType, data, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if frameType != FrameScreenshotAck {
		t.Errorf("frame type: got 0x%02x, want 0x%02x", frameType, FrameScreenshotAck)
	}

	gotTS, ok := ParseScreenshotAck(data)
	if !ok {
		t.Fatal("ParseScreenshotAck: ok=false")
	}
	if !gotTS.Equal(ts.Truncate(time.Millisecond)) {
		t.Errorf("timestamp: got %v, want %v", gotTS, ts)
	}
}

func TestParseScreenshotAckWrongSize(t *testing.T) {
	_, ok := ParseScreenshotAck([]byte("too-short"))
	if ok {
		t.Error("expected ok=false for wrong-size payload")
	}
	_, ok = ParseScreenshotAck(nil)
	if ok {
		t.Error("expected ok=false for nil payload")
	}
}
