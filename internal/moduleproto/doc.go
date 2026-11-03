// Package moduleproto defines the wire protocol for the agent module
// registry. It specifies the control-stream messages, catalog snapshot
// framing, invocation stream markers, error codes, and protocol limits.
//
// The protocol is additive: it activates only when both peers negotiate
// the "modules" capability. Old peers ignore the capability and continue
// with the existing byte protocols.
package moduleproto
