# Module API Policy

Pure request/response policy shared by every surface that exposes module capabilities: the console routes (`internal/consoleserver`), the agent's loopback API (`internal/agent/module_api.go`), and the browser client proxy (`internal/clientserver/module_proxy.go`). No transport, no authentication, no `http.Client` — each caller owns those.

## Files

- `http.go` — method resolution, URL/path handling, request sanitization, response header allowlist, JSON helper, correlation IDs.
- `assets.go` — declared-asset lookup and content verification (size + SHA-256), safe asset path cleaning, content-type inference.
- `http_test.go` — pure-function tests only (no sockets, no mocks), per project policy.

## Request Policy

| Concern | Rule |
|---------|------|
| Query string | `BuildRequest` copies `src.URL.RawQuery` verbatim. Module query parameters are part of the operation contract; dropping or re-encoding them silently changes the call. |
| Operation path | Take it from `TrailAfterSegments(r.URL.EscapedPath(), prefix, n)` — never rebuild it from route variables. The router URL-decodes variables, so a `%2F` inside a path parameter would turn into a segment separator. |
| Method override | `ResolveMethod` converts **only** `POST` + `X-SSET-Method-Override: TRACE` into `TRACE`. Browsers refuse to send TRACE (and CONNECT and friends) from `fetch`, but an OpenAPI contract may declare it. Every other method keeps its own verb. |
| Body | `req.ContentLength = src.ContentLength` so a module honouring `Content-Length` sees the framing the caller sent instead of a chunked body. |
| Headers | `CopyRequestHeaders` drops hop-by-hop headers, surface credentials (`authorization`, `cookie`, `x-sset-token`, `x-sset-launch-secret`), forwarding headers, browser Fetch Metadata headers, the override header, and `Accept-Encoding`. The surface, not the module, decides wire encoding: an encoded module body relayed without `Content-Encoding` is unreadable. |
| Correlation | `BuildRequest` generates an `X-Correlation-ID` when the caller supplied none; the module echoes it so a local invocation can be matched to its log line. |
| Preflight | `IsPreflight` requires `OPTIONS` **and** `Access-Control-Request-Method`. A bare `OPTIONS` is an ordinary request and must reach a module that declares an OPTIONS operation. |

## Response Policy

`ResponseAllowlist` is exactly: `content-type`, `content-length`, `content-disposition`, `etag`, `last-modified`, `x-request-id`, `location`, `x-catalog-epoch`.

- `cache-control` is deliberately absent. Module output on the console and client surfaces is per-caller, the URL carries no caller identity, and nothing sets `Vary`; honouring a module's `public, max-age=86400` would let a shared cache answer another caller from the same URL. Each surface keeps its own `no-store` floor.
- `x-catalog-epoch` is a version tag naming the registry snapshot, not caller data, so it may cross the proxy (the UI shows which snapshot answered).
- `CopyResponseHeader` deletes the key before appending: surfaces pre-declare defaults, and appending would ship two values that disagree.

## Asset Policy

UI assets are trusted only when the module's own descriptor declares them.

- `FindDeclaredAsset` matches a request path against `AssetEntry{Path,Size,Digest}`, tolerating an optional `ui/` prefix.
- `ReadVerifiedAsset` refuses symlinks and paths that escape the package directory, reads at most `Size+1` bytes to detect an oversized file, then `VerifyAssetContent` re-hashes and compares size and SHA-256. A mismatch is a `502` at the surface: the bytes on disk are not the bytes the package promised.
- `ContentTypeForPath` derives the type from the extension; SVG assets additionally get a restrictive CSP at every surface.

## Rules

* Anything that changes browser→module semantics belongs here, in one place, not in each surface.
* Keep this package free of I/O and infrastructure imports; a caller that needs a client or a listener owns it.
* Never let a surface add an unauthenticated route to the public tunnel listener. Only the console (user auth) and the loopback surfaces (loopback + launch secret/fetch metadata) serve modules.
* New response headers must be added to the allowlist deliberately, with the cache/leak argument written down.
