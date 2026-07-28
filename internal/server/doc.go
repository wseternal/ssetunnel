// Package server runs the public side of the tunnel: HTTP handlers for
// the SSE downstream and batched POST upstream, a session registry, and
// the TCP agent listener that users connect to.
package server
