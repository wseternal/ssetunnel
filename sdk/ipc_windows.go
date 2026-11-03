//go:build windows

package sdk

// ipcNetwork is the network type for IPC connections on Windows.
// Windows named pipes use "unix" for compatibility via go-winio.
const ipcNetwork = "unix"
