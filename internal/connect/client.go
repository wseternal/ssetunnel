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

	"github.com/cenkalti/backoff/v4"
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
// the provided reader/writer and the server connection. When the server
// side closes (EOF on read), the connection is torn down immediately so
// that the writer's consumer (e.g. an SSH client reading from a pipe)
// also sees EOF and can react. When the reader reaches EOF first, a TCP
// half-close is used to signal the remote side without killing the read
// path.
func (c *Client) ServeRW(ctx context.Context, r io.Reader, w io.Writer) error {
	serverConn, err := c.dialAndHandshake(ctx)
	if err != nil {
		return fmt.Errorf("stdio connect handshake failed: %w", err)
	}
	defer serverConn.Close()

	// r → server in the background; half-close on reader EOF so the
	// remote target sees EOF. This goroutine may outlive ServeRW if
	// the server side closes first (see below), which is fine for
	// ProxyCommand usage where the process exits on return.
	go func() {
		buf := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(buf)
		io.CopyBuffer(serverConn, r, *buf)
		if tcpConn, ok := serverConn.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
	}()

	// server → w: when the server side closes (EOF or error), return
	// immediately. This tears down the process (in ProxyCommand mode),
	// closing stdout so the parent process (SSH client) sees EOF and
	// can react. Waiting for r→server here would deadlock when the
	// reader is a terminal waiting for user input that will never come
	// because the remote side already closed.
	buf := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(buf)
	_, err = io.CopyBuffer(w, serverConn, *buf)
	return err
}

func (c *Client) handleLocalConn(ctx context.Context, localConn net.Conn) {
	defer localConn.Close()

	// Retry dialAndHandshake with exponential backoff so transient
	// server outages don't immediately kill the user's connection.
	var serverConn net.Conn
	err := backoff.Retry(func() error {
		if ctx.Err() != nil {
			return backoff.Permanent(ctx.Err())
		}
		var dialErr error
		serverConn, dialErr = c.dialAndHandshake(ctx)
		if dialErr != nil {
			log.Printf("connect: handshake failed (will retry): %v", dialErr)
			return dialErr
		}
		return nil
	}, backoff.WithContext(clientBackoff(), ctx))
	if err != nil {
		log.Printf("connect: handshake failed after retries: %v", err)
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

// clientBackoff builds the exponential backoff strategy for connect
// client retries: 500 ms initial, 1.5× multiplier, 30 s total cap.
func clientBackoff() *backoff.ExponentialBackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 500 * time.Millisecond
	b.MaxInterval = 10 * time.Second
	b.MaxElapsedTime = 30 * time.Second
	b.Multiplier = 1.5
	b.RandomizationFactor = 0.1
	return b
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
