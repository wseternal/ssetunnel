# Next Phases — Deferred Improvements

## Versioned TCP Handshake (`v2:`)

### Current Protocol

```
Client → Server:  <token>\n
Server → Client:  OK\n          (success)
                  <closes>      (failure)
```

The first bytes on the TCP connection are the raw token string followed by a newline. After `OK\n`, the connection transitions to bidirectional proxy.

### Problem

Zero extensibility. No framing, no version byte, no message type, no way to carry additional context.

### Proposed `v2:` Protocol

```
Client → Server:  v2:{"token":"...","intent":"connect","agent_id":"..."}\n
Server → Client:  v2:{"status":"ok","session_id":"..."}\n
                  v2:{"status":"err","reason":"unauthorized"}\n
```

Server detects the `v2:` prefix and routes to different validation logic. Old clients (bare token) continue on the legacy path.

### When This Is Needed

- **Agent targeting** in the connect handshake (e.g., "connect me to agent `abc123`")
- **Mutual authentication** (server proves its identity to the client before proxy starts)
- **Connection multiplexing** over the entry port (one TCP, multiple user sessions)
- **Replay protection** (nonce + timestamp in the handshake)

### Why It Was Deferred

1. Session tokens are opaque — server resolves via DB lookup regardless of wire format
2. Intent is already disambiguated by transport (entry listener = connect, HTTP endpoint = agent)
3. The migration works without wire changes — `authenticateEntryConn` tries `ValidateUserSession` first, falls back to `ValidateToken`
4. Dual-protocol burden (parse two formats, test both paths) is premature complexity

### Implementation Notes

When ready:
- Add `v2:` prefix detection in `authenticateEntryConn` (`internal/server/server.go`)
- Parse JSON payload for intent + metadata
- Old bare-token path remains as fallback until deprecated
- Client (`internal/connect/client.go`) sends `v2:` when connecting to a server that supports it (capability negotiation via probe or server version header)
