# Session Token Refresh

## Goal
Session tokens obtained via `ssetunnel login` are transparently refreshed before expiration so users never experience authentication failures during normal use.

## Context
- `ssetunnel login` creates a session token with a **30-day TTL** stored in the `user_sessions` PostgreSQL table.
- The token is saved client-side to `~/.ssetunnel/session` (JSON with `token`, `username`, `role` — no expiry metadata).
- When the token expires, the server's `ValidateUserSession` SQL query (`expires_at > CURRENT_TIMESTAMP`) rejects it with `401 Unauthorized`.
- Users only discover expiration when their `ssetunnel agent` or `ssetunnel connect` command fails.
- There is no refresh mechanism — users must re-run `ssetunnel login` manually.

## Success Criteria
- Session tokens are automatically refreshed before expiration during normal agent/connect operation.
- The refresh is invisible to the user — no re-login prompts, no configuration changes, no workflow interruptions.
- The client-side session file (`~/.ssetunnel/session`) is updated with the new token after a successful refresh.
- Refreshed tokens carry the same permissions (role, user_id) as the original.
- If refresh is impossible (e.g., user disabled, account deleted, explicitly logged out), the user gets a clear error message directing them to re-run `ssetunnel login`.

## Constraints
- The refresh mechanism must not weaken security — a revoked or disabled user must not be able to refresh.
- The existing 30-day TTL policy remains; refresh extends from the current time, not from original creation.
- Server-side changes must be backward-compatible with existing clients that don't support refresh (they continue to work until their token naturally expires).
- The refresh endpoint must be rate-limited to prevent abuse.

## Out of Scope
- Changing the 30-day TTL duration (this is a policy decision, not a mechanism one).
- Refresh for standalone API tokens (from the `tokens` table) — only user session tokens from `ssetunnel login`.
- Token refresh UI in the console frontend.
- Refresh tokens as a separate token type (sliding window approach preferred for simplicity).

## Created
2026-08-30
