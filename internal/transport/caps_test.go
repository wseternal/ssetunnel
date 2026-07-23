package transport_test

import (
	"testing"

	"github.com/wseternal/ssetunnel/internal/transport"
)

func TestParseCaps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		header string
		want   transport.Caps
	}{
		{"absent", "", transport.Caps{}},
		{"full", "concurrency=4;batch=1048576;gzip", transport.Caps{Concurrency: 4, Batch: 1048576, Gzip: true}},
		{"malformed header", ";;;", transport.Caps{}},
		{"malformed value", "concurrency=x", transport.Caps{}},
		{"negative concurrency", "concurrency=-2", transport.Caps{}},
		{"malformed batch keeps other axes", "batch=abc;gzip", transport.Caps{Gzip: true}},
		{"bare gzip flag", "gzip", transport.Caps{Gzip: true}},
		{"unknown keys ignored", "foo=bar;concurrency=3", transport.Caps{Concurrency: 3}},
		{"whitespace tolerated", " concurrency=4 ; gzip ", transport.Caps{Concurrency: 4, Gzip: true}},
		{"gzip value ignored", "gzip=true;concurrency=2", transport.Caps{Concurrency: 2, Gzip: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := transport.ParseCaps(tt.header); got != tt.want {
				t.Fatalf("ParseCaps(%q) = %+v, want %+v", tt.header, got, tt.want)
			}
		})
	}
}

func TestNegotiateCaps(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		want       transport.Caps
		advertised transport.Caps
		negotiated transport.Caps
	}{
		{
			"per-axis min",
			transport.Caps{Concurrency: 4, Batch: 65536, Gzip: true},
			transport.Caps{Concurrency: 4, Batch: 1048576, Gzip: true},
			transport.Caps{Concurrency: 4, Batch: 65536, Gzip: true},
		},
		{
			"server clamps agent",
			transport.Caps{Concurrency: 8, Batch: 2 << 20, Gzip: true},
			transport.Caps{Concurrency: 4, Batch: 1048576, Gzip: true},
			transport.Caps{Concurrency: 4, Batch: 1048576, Gzip: true},
		},
		{
			"gzip needs both sides",
			transport.Caps{Concurrency: 4, Gzip: true},
			transport.Caps{Concurrency: 4},
			transport.Caps{Concurrency: 4},
		},
		{
			"absent server caps fail closed to cycle-1 profile",
			transport.Caps{Concurrency: 4, Batch: 65536, Gzip: true},
			transport.Caps{},
			transport.Caps{Concurrency: 1, Batch: transport.DefaultMaxBatchSize},
		},
		{
			"absent batch axis fails closed to 16 KiB",
			transport.Caps{Concurrency: 4, Batch: 65536, Gzip: true},
			transport.Caps{Concurrency: 4},
			transport.Caps{Concurrency: 4, Batch: transport.DefaultMaxBatchSize},
		},
		{
			"serial agent stays serial",
			transport.Caps{Concurrency: 1, Batch: 16384},
			transport.Caps{Concurrency: 4, Batch: 1048576, Gzip: true},
			transport.Caps{Concurrency: 1, Batch: 16384},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := transport.NegotiateCaps(tt.want, tt.advertised); got != tt.negotiated {
				t.Fatalf("NegotiateCaps(%+v, %+v) = %+v, want %+v",
					tt.want, tt.advertised, got, tt.negotiated)
			}
		})
	}
}

func TestCapsStringRoundTrip(t *testing.T) {
	t.Parallel()
	tests := []transport.Caps{
		{},
		{Concurrency: 1, Batch: 16384},
		{Concurrency: 4, Batch: 65536, Gzip: true},
	}
	for _, want := range tests {
		if got := transport.ParseCaps(want.String()); got != want {
			t.Fatalf("ParseCaps(%q) = %+v, want %+v", want.String(), got, want)
		}
	}
	if s := (transport.Caps{Concurrency: 4, Batch: 65536, Gzip: true}).String(); s != "concurrency=4;batch=65536;gzip" {
		t.Fatalf("String() = %q, want %q", s, "concurrency=4;batch=65536;gzip")
	}
}
