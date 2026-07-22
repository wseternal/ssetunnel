package mux_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/wseternal/ssetunnel/internal/mux"
	"github.com/wseternal/ssetunnel/internal/server"
	"github.com/wseternal/ssetunnel/internal/transport"
)

// setupMux builds a mux client/server pair over the REAL tunnel: agent
// transport.Conn ↔ internal/server handlers via httptest.
func setupMux(t *testing.T) (client, serverSide *yamux.Session) {
	t.Helper()
	reg := server.NewRegistry()
	srv := httptest.NewServer(server.NewHandler(reg, time.Hour))
	t.Cleanup(srv.Close)

	conn, err := transport.DialAgent(context.Background(), transport.Config{
		URL:       srv.URL,
		SessionID: "mux-test",
		MaxWait:   time.Millisecond,
	})
	if err != nil {
		t.Fatalf("DialAgent: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	var sess *server.Session
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if sess = reg.Get("mux-test"); sess != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if sess == nil {
		t.Fatal("session never registered")
	}

	client, err = mux.Client(conn)
	if err != nil {
		t.Fatalf("mux.Client: %v", err)
	}
	t.Cleanup(func() { client.Close() })
	serverSide, err = mux.Server(sess)
	if err != nil {
		t.Fatalf("mux.Server: %v", err)
	}
	t.Cleanup(func() { serverSide.Close() })
	return client, serverSide
}

// acceptLoop feeds accepted streams to handle until the session dies.
func acceptLoop(s *yamux.Session, handle func(net.Conn)) {
	for {
		st, err := s.Accept()
		if err != nil {
			return
		}
		go handle(st)
	}
}

func TestMuxSessionEstablishment(t *testing.T) {
	t.Parallel()
	client, serverSide := setupMux(t)
	accepted := make(chan net.Conn, 1)
	go func() {
		st, err := serverSide.Accept()
		if err == nil {
			accepted <- st
		}
	}()
	st, err := client.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("server never accepted the stream")
	}
}

func TestMuxStreamEcho(t *testing.T) {
	t.Parallel()
	client, serverSide := setupMux(t)
	go acceptLoop(serverSide, func(st net.Conn) { io.Copy(st, st) })

	st, err := client.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	st.SetDeadline(time.Now().Add(30 * time.Second))
	want := bytes.Repeat([]byte("mux-echo-pattern-0123456789"), 1000) // ~29 KB
	if _, err := st.Write(want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(st, got); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("echo mismatch: got %d bytes, want %d", len(got), len(want))
	}
}

func TestMuxNoHeadOfLine(t *testing.T) {
	t.Parallel()
	client, serverSide := setupMux(t)

	// First stream is the stalled one: server never reads from it.
	parked := make(chan net.Conn, 1)
	var once sync.Once
	go acceptLoop(serverSide, func(st net.Conn) {
		once.Do(func() { parked <- st }) // stall: never read, never close
		io.Copy(st, st)
	})

	// Stalled stream: client writes 64 KiB (fits the window), never reads.
	stall, err := client.Open()
	if err != nil {
		t.Fatalf("Open stall: %v", err)
	}
	defer stall.Close()
	stall.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := stall.Write(bytes.Repeat([]byte{'s'}, 64<<10)); err != nil {
		t.Fatalf("stalled stream Write: %v", err)
	}

	// 31 other streams must complete full echoes concurrently.
	const streams = 31
	const payload = 256 << 10
	var wg sync.WaitGroup
	errs := make(chan error, streams)
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, err := client.Open()
			if err != nil {
				errs <- fmt.Errorf("open %d: %w", i, err)
				return
			}
			defer st.Close()
			st.SetDeadline(time.Now().Add(60 * time.Second)) // generous
			want := bytes.Repeat([]byte{byte(i)}, payload)
			if _, err := st.Write(want); err != nil {
				errs <- fmt.Errorf("write %d: %w", i, err)
				return
			}
			got := make([]byte, payload)
			if _, err := io.ReadFull(st, got); err != nil {
				errs <- fmt.Errorf("read %d: %w", i, err)
				return
			}
			if !bytes.Equal(got, want) {
				errs <- fmt.Errorf("echo %d mismatch", i)
			}
		}(i)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("streams did not complete: stalled reader caused head-of-line blocking")
	}
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestMuxWindowProof(t *testing.T) {
	t.Parallel()
	client, serverSide := setupMux(t)
	// Server accepts but never reads: the write can only complete if the
	// stream window exceeds the 256 KiB yamux default (plan decision 6).
	go acceptLoop(serverSide, func(st net.Conn) { select {} })

	st, err := client.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	st.SetWriteDeadline(time.Now().Add(60 * time.Second))
	const payload = 512 << 10 // 2x the default window
	if _, err := st.Write(bytes.Repeat([]byte{'w'}, payload)); err != nil {
		t.Fatalf("Write of %d KiB without reads: %v (window too small?)", payload>>10, err)
	}
}
