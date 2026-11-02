//go:build !windows

package sdk

// ipcNetwork is the network type for IPC connections on Unix-like systems.
const ipcNetwork = "unix"
