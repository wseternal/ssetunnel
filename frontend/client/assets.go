// Package client embeds the client SPA bundle. It is separate from the
// console embed (package frontend) so server binaries never carry client
// assets and client binaries never carry console assets (ADR-005).
package client

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var clientFS embed.FS

// ClientWebRootFs is the embedded client SPA filesystem shared by the
// CLI browser server and the Clix desktop app.
var ClientWebRootFs fs.FS

func init() {
	var err error
	if ClientWebRootFs, err = fs.Sub(clientFS, "dist"); err != nil {
		panic(err)
	}
}