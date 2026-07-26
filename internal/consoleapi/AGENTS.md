# Console API

JSON management API for the admin console. Mounted at `/api/v1/` under the console HTTP server.

## Routes

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/api/v1/login` | Public | Username/password login → returns session token |
| POST | `/api/v1/user-login` | Public | Same as login, alternative endpoint |
| POST | `/api/v1/logout` | Admin | Clears session cookie, revokes user session |
| GET | `/api/v1/me` | Admin | Validate current session, return user info |
| GET/POST | `/api/v1/tokens` | Admin | List or create bearer tokens |
| DELETE | `/api/v1/tokens/{id}` | Admin | Revoke a token by ID |
| POST | `/api/v1/enroll` | Admin | Create PIN + optional TOTP QR code |
| GET | `/api/v1/sessions` | Admin | List live tunnel sessions with stats |
| GET/POST | `/api/v1/agents` | Admin | List or create agent configs |
| PATCH/DELETE | `/api/v1/agents/{id}` | Admin | Update or delete agent config |
| GET | `/api/v1/users` | Admin | List users |
| POST | `/api/v1/users` | Admin | Create user |
| PATCH | `/api/v1/users/{id}` | Admin | Update user |
| DELETE | `/api/v1/users/{id}` | Admin | Delete user |

## Key Endpoints

### `POST /api/v1/enroll`
Creates a single-use PIN (30 min TTL) and optionally returns a TOTP QR code (PNG, base64-encoded) for agent setup.

### `GET /api/v1/agents`
Lists all agent configs including the NULL default row.

### `POST /api/v1/agents`
Creates a new agent config with `agent_id`, `description`, and `allowed_targets`.

### `PATCH /api/v1/agents/{id}`
Updates an agent config. Cannot rename the NULL default row to a named `agent_id` (`ErrCannotRenameDefault`).

### `DELETE /api/v1/agents/{id}`
Deletes an agent config by ID. Cannot delete the NULL default row (`ErrCannotDeleteDefault`).

### `GET /api/v1/users`
Lists all users with their permissions.

### `POST /api/v1/users`
Creates a new user with username, password hash, role, and permission flags.

### `GET /api/v1/me`
Validates the current session token and returns user info. Used by the frontend to restore login state on page refresh.

## Rules
* Protected routes use `AdminSessionMiddleware` (bearer token or session cookie).
* Login uses username/password authentication with optional TOTP.
* Logout revokes the server-side session via `RevokeUserSession`.
* The NULL default agent config row cannot be deleted or renamed.
