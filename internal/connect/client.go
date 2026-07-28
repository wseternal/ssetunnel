package connect

import (
	"bufio"
	"context"
	"fmt"
	"io"
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
	serverConn, err := c.dialAndHandshake(ctx)
	if err != nil {
		return fmt.Errorf("stdio connect handshake failed: %w", err)
	}
	defer serverConn.Close()

	errCh := make(chan error, 2)

	go func() {
		buf := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(buf)
		_, err := io.CopyBuffer(serverConn, os.Stdin, *buf)
		errCh <- err
	}()

	go func() {
		buf := bufferPool.Get().(*[]byte)
		defer bufferPool.Put(buf)
		_, err := io.CopyBuffer(os.Stdout, serverConn, *buf)
		errCh <- err
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		return err
	}
}

func (c *Client) handleLocalConn(ctx context.Context, localConn net.Conn) {
	defer localConn.Close()

	serverConn, err := c.dialAndHandshake(ctx)
	if err != nil {
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
