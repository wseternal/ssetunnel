# Console API

JSON management API for the admin console. Mounted at `/api/v1/` under the console HTTP server.

## Routes

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| POST | `/api/v1/login` | Public | TOTP login → sets session cookie (12 h) |
| POST | `/api/v1/logout` | Public | Clears session cookie |
| GET/POST | `/api/v1/tokens` | Admin | List or create bearer tokens |
| DELETE | `/api/v1/tokens/{id}` | Admin | Revoke a token by ID |
| POST | `/api/v1/enroll` | Admin | Create PIN + optional TOTP QR code |
| GET | `/api/v1/sessions` | Admin | List live tunnel sessions with stats |

## Key Endpoints

### `POST /api/v1/enroll`
Creates a single-use PIN (30 min TTL) and optionally returns a TOTP QR code (PNG, base64-encoded) for agent setup.

### `GET /api/v1/sessions`
Iterates the `Registry` and returns session ID, bytes sent/received, creation time, and remote address.

## Rules
* Protected routes use `AdminSessionMiddleware` (bearer token or session cookie).
* Login uses `Auth.VerifyTOTP` only when `totpSecret` is configured.
