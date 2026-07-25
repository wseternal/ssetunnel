package connect

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

var bufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 32*1024)
		return &buf
	},
}

type Client struct {
	serverEntryAddr string
	token           string
}

func NewClient(serverEntryAddr string, token string) *Client {
	return &Client{
		serverEntryAddr: serverEntryAddr,
		token:           token,
	}
}

func (c *Client) ServeListener(ctx context.Context, ln net.Listener) error {
	// Eagerly validate the token by performing a test handshake at startup.
	// This catches invalid tokens immediately instead of silently failing
	// on the first user connection.
	if c.token != "" {
		probe, err := c.dialAndHandshake(ctx)
		if err != nil {
			return fmt.Errorf("token validation failed: %w", err)
		}
		probe.Close()
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go c.handleLocalConn(ctx, conn)
	}
}

func (c *Client) ServeStdio(ctx context.Context) error {
	return c.ServeRW(ctx, os.Stdin, os.Stdout)
}

// ServeRW connects to the entry server and copies bidirectionally between
// the provided reader/writer and the server connection. It handles
// half-close correctly: when the reader reaches EOF, the server's write
// side is closed (via TCP half-close) so the remote target sees EOF,
// and the function waits for both directions to finish before returning.
func (c *Client) ServeRW(ctx context.Context, r io.Reader, w io.Writer) error {
	serverConn, err := c.dialAndHandshake(ctx)
	if err != nil {
		return fmt.Errorf("stdio connect handshake failed: %w", err)
	}
	defer serverConn.Close()

	var wg sync.WaitGroup
	errCh := make(chan error, 2)

	// stdin → server; half-close on EOF so the remote side sees it.
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(buf)
		_, err := io.CopyBuffer(serverConn, r, *buf)
		if tcpConn, ok := serverConn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
		errCh <- err
	}()

	// server → stdout
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(buf)
		_, err := io.CopyBuffer(w, serverConn, *buf)
		errCh <- err
	}()

	// Wait for both directions, return first non-nil error.
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) handleLocalConn(ctx context.Context, localConn net.Conn) {
	defer localConn.Close()

	serverConn, err := c.dialAndHandshake(ctx)
	if err != nil {
		log.Printf("connect: handshake failed: %v", err)
		return
	}
	defer serverConn.Close()

	go func() {
		buf := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(buf)
		_, _ = io.CopyBuffer(serverConn, localConn, *buf)
		_ = serverConn.Close()
	}()

	buf := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(buf)
	_, _ = io.CopyBuffer(localConn, serverConn, *buf)
}

func (c *Client) dialAndHandshake(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", c.serverEntryAddr)
	if err != nil {
		return nil, fmt.Errorf("dial entry %s: %w", c.serverEntryAddr, err)
	}

	if c.token != "" {
		if _, err := fmt.Fprintf(conn, "%s\n", c.token); err != nil {
			conn.Close()
			return nil, fmt.Errorf("send token: %w", err)
		}

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(conn)
		respLine, err := reader.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("read handshake response: %w", err)
		}

		if strings.TrimSpace(respLine) != "OK" {
			conn.Close()
			return nil, fmt.Errorf("handshake rejected: %s", strings.TrimSpace(respLine))
		}
		conn.SetReadDeadline(time.Time{})
	}

	return conn, nil
}
