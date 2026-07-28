// Package agent runs inside the restricted network: it dials out to the
// server, multiplexes streams over the tunnel, forwards them to the
// configured TCP target, and reconnects automatically on drops.
package agent
