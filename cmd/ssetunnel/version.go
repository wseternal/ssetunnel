package main

import "fmt"

// Version is the application version. Set at build time via -ldflags:
//
//	go build -ldflags "-X main.Version=0.1.0"
var Version = "dev"

// GitSHA is the short git commit SHA. Set at build time via -ldflags:
//
//	go build -ldflags "-X main.GitSHA=$(git rev-parse --short HEAD)"
var GitSHA = ""

// BuildVersion returns a human-readable version string suitable for display
// in banners and the "version" command. It includes the git SHA when available
// so users can identify exactly which revision is running.
func BuildVersion() string {
	if GitSHA != "" {
		return fmt.Sprintf("%s (%s)", Version, GitSHA)
	}
	return Version
}
