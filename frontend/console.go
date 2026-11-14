package frontend

import (
	"embed"
	"io/fs"
)

//go:embed all:console/dist
var consoleFS embed.FS

// ConsoleWebRootFs is the embedded admin console SPA filesystem.
var ConsoleWebRootFs fs.FS

func init() {
	var err error
	if ConsoleWebRootFs, err = fs.Sub(consoleFS, "console/dist"); err != nil {
		panic(err)
	}
}
