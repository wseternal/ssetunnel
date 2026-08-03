package remoteapp

import (
	"bytes"
	"encoding/json"
	"testing"
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
