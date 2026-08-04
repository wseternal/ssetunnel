package agent

// TargetShell is the magic target name that tells the agent to spawn
// an interactive shell with a PTY instead of dialing a TCP address.
// The agent reads this from the stream header (dynamic target mode)
// and spawns the user's configured shell ($SHELL or /bin/sh).
const TargetShell = "__shell__"

// TargetRemoteApp is the magic target name that tells the agent to
// start a remote desktop session (screen capture + input replay)
// instead of dialing a TCP address.
const TargetRemoteApp = "__remote_app__"
