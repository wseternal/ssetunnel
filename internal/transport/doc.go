// Package transport implements the wire layer of the tunnel: an SSE
// codec for the downstream direction, an eager-flush batcher for the
// upstream direction, and a net.Conn adapter that combines them with
// serial POSTs.
package transport
