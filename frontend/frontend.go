package frontend

import (
	"embed"
	"io/fs"
)

//go:embed console/dist
var consoleFS embed.FS

var ConsoleWebRootFs fs.FS

func init() {
	var err error
	if ConsoleWebRootFs, err = fs.Sub(consoleFS, "console/dist"); err != nil {
		panic(err)
	}
}
