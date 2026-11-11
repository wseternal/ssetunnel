package frontend

import (
	"embed"
	"io/fs"
)

//go:embed client/dist
var clientFS embed.FS

// ClientWebRootFs is the embedded client SPA filesystem shared by
// the CLI browser server and the Clix desktop app.
var ClientWebRootFs fs.FS

func init() {
	var err error
	if ClientWebRootFs, err = fs.Sub(clientFS, "client/dist"); err != nil {
		panic(err)
	}
}
