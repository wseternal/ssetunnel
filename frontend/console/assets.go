package console

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var consoleFS embed.FS

// ConsoleWebRootFs is the embedded admin console SPA filesystem.
var ConsoleWebRootFs fs.FS

func init() {
	var err error
	if ConsoleWebRootFs, err = fs.Sub(consoleFS, "dist"); err != nil {
		panic(err)
	}
}
