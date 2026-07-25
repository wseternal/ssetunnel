package main

import "testing"

func TestBuildVersion_WithGitSHA(t *testing.T) {
	origVersion := Version
	origGitSHA := GitSHA
	defer func() { Version = origVersion; GitSHA = origGitSHA }()

	Version = "0.1.0"
	GitSHA = "abc1234"

	got := BuildVersion()
	want := "0.1.0 (abc1234)"
	if got != want {
		t.Errorf("BuildVersion() = %q, want %q", got, want)
	}
}

func TestBuildVersion_WithoutGitSHA(t *testing.T) {
	origVersion := Version
	origGitSHA := GitSHA
	defer func() { Version = origVersion; GitSHA = origGitSHA }()

	Version = "0.1.0"
	GitSHA = ""

	got := BuildVersion()
	want := "0.1.0"
	if got != want {
		t.Errorf("BuildVersion() = %q, want %q", got, want)
	}
}

func TestBuildVersion_DefaultDev(t *testing.T) {
	origVersion := Version
	origGitSHA := GitSHA
	defer func() { Version = origVersion; GitSHA = origGitSHA }()

	Version = "dev"
	GitSHA = ""

	got := BuildVersion()
	want := "dev"
	if got != want {
		t.Errorf("BuildVersion() = %q, want %q", got, want)
	}
}
