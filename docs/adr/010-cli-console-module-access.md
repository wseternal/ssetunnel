# ADR-010: CLI Console Module Access

## Status

Accepted. Implemented in `67f8474` and refined in `f9fd394`, `c051e10`, and `f0a87b3`.

## Date

2026-11-27

## Context

The console module surface already existed. The console page could list connected agents, list an agent's modules, read operation models and the OpenAPI document, and invoke operations. The CLI could only manage modules on the local agent host through `cli module`. It had no command to inspect or call a module through the console.

That gap had three costs:

1. **Scripts hand-rolled the HTTP surface.** A caller had to read the session file, build the console base URL, add the bearer token, escape agent and module IDs, and forward headers and body. Each script re-derived rules that the console and `moduleapi` already own.
2. **Discovery lived only in the browser.** A terminal user could not answer "which agents are connected" or "which operations does this module expose" without opening the console.
3. **The `cli ui` proxy is not a remote tool.** It resolves the agent from the local status file. It cannot reach an agent on another machine.

The console routes needed no extension:

| Purpose | Route |
|---|---|
| List agents | `GET /api/v1/connected-agents` |
| List modules | `GET /api/v1/modules?agent=<id>` |
| Operation models | `GET /api/v1/modules/<agent>/<module>/operations` |
| OpenAPI document | `GET /api/v1/modules/openapi/<agent>/<module>` |
| Invoke | any method on `/api/v1/modules/invoke/<agent>/<module>/<path>` |

## Decision

### 1. Add a `console` command group to `cmd/cli`

`cmd/cli` gains five subcommands:

```
cli console list-agents [--json]
cli console list-modules [<agent-id>] [--json]
cli console schema  [<agent-id>] <module-id>
cli console openapi [<agent-id>] <module-id>
cli console call    [<agent-id>] <module-id> (--operation <id> | --method <m> --path <p>)
```

`cmd/ssetunnel` stays unchanged. That binary is the frozen compatibility surface for installed services and SSH `ProxyCommand`. New client commands belong to `cmd/cli`. `command.CLIUsage` lists `console`. `command.Usage` does not change.

### 2. Use the console API with the saved login session

The CLI talks to the console, not to the agent's loopback API. It resolves the session with `clientapp.ResolveSession`. `--server` sets the console base URL and wins over the stored `console_url`. A missing token is allowed, because an auth-disabled dev server needs none. An auth-enabled server answers 401 on a catalog route, and the CLI tells the user to run login. A 401 from the invoke route keeps its own status. It does not add that hint.

The base is `<console-url>/api/v1`. A console behind a path prefix must include that prefix in the URL, for example `https://host/console`. This matches `consoleAPIBase`, which the login flow already uses.

### 3. Keep transport and presentation apart

- `internal/clientapp/console.go` owns the HTTP client, session resolution, URL validation, request construction, and error mapping. Methods: `ListAgents`, `ListModules`, `Operations`, `OpenAPI`, `Invoke`.
- `internal/clicommand/console.go` owns flags, positional parsing, agent inference, tables, JSON output, body rendering, and exit codes.
- Server code does not change. `internal/moduleapi` remains the single owner of request and response policy for every module surface.

### 4. Target the agent explicitly or by inference

- One positional argument is the module ID. Two positional arguments are the agent ID then the module ID.
- With no agent argument, the command uses the only connected agent. It prints the resolved ID to stderr, so a script never guesses which agent it reached.
- Zero agents is an error with a connect hint. Two or more agents is an error that lists the candidate IDs. An explicit ID always wins.

### 5. Define the call grammar and output contract

- `--method` and `--path` are one form. `--path` carries the query string and percent escapes unchanged.
- `--operation <id>` is the other form. It resolves method and path from the operation document. Using both forms is an error. An operation with a templated path asks for `--method` and `--path` with concrete values.
- `-H "Name: value"` repeats. `-d` accepts inline text, `@file`, and `@-`. A body without `Content-Type` defaults to `application/json`. A `@file` or `@-` body is capped at 32 MiB.
- The response body goes to stdout. A JSON body under 8 MiB is indented only when stdout is a terminal. Piped output stays raw and byte-exact.
- `-i` prints the status line and response headers before the body.
- 2xx exits 0. A non-2xx status prints the body on stdout, writes the method, path without query values, agent, module, status, console host and correlation ID to stderr, and exits 1. A 401 on a catalog route adds the re-login step. A 401 from the invoke route keeps its own status. A transport error exits 1.

### 6. Apply one security contract

- Every console URL passes `auth.ValidateConsoleURL`. Non-loopback hosts need HTTPS. Link-local hosts are refused. The console client also rejects a missing host, userinfo, a query, and a fragment before a request or a log. A rejected credential never appears in the error.
- The saved token is used only when the resolved host matches a stored session. The match is by hostname, not by origin: a console on the same host but another port or path prefix still receives the stored bearer, because `login` stores the entry under the derived tunnel URL while `console_url` carries the console port. A different host gets no token at all.
- An explicit `--server` selects the base URL only. It never becomes the session's stored server URL, so a refresh triggered by a console command writes back the tunnel endpoint rather than the console endpoint.
- Redirects are refused. A bearer token never follows a `Location` header to another host.
- Custom headers are copied first, then the session bearer is set. When a session token exists, `-H Authorization` cannot replace it.
- Transport errors unwrap `*url.Error`, so query values do not appear in error text. Status errors print the path without query values.
- Console error bodies are capped at 8 KiB. JSON reads are capped at 32 MiB.
- Every request carries `X-Correlation-Id`, either caller-supplied or generated.

## Alternatives Considered

### Top-level `cli call`

Rejected. The feature also needs agent and module discovery. A `console` group gives those commands one home and matches the console page they mirror. The requirement included discovery commands, so a single top-level call command did not fit.

### Call the agent's loopback module API

Rejected. It only works on the agent host and needs the agent status file. It also uses a different trust boundary. A terminal on another machine cannot use it. The console API already provides authentication, routing, and the same `moduleapi` policy.

### Publish curl recipes instead of a command

Rejected. Recipes still require manual token handling, URL prefix handling, and route escaping. They drift from the console routes.

### Add a server-side CLI endpoint

Rejected. The console routes already expose every needed operation. A second endpoint would duplicate routing, authorization, and response policy.

### Always infer the agent

Rejected. Silent selection becomes wrong as soon as a second agent connects. The explicit ID plus a stderr announcement keeps scripts honest.

### Operation-ID-only invocation

Rejected. Operation IDs are optional in OpenAPI, and callers often need a concrete path with query parameters. `--method` and `--path` stay available.

### Raw output only, or pretty output always

Rejected. Raw-only hurts terminal reading. Pretty-always breaks pipes. The TTY check gives both behaviours without a flag.

### Follow redirects

Rejected. It would forward the bearer token to whatever host the console names.

## Consequences

- The CLI reaches the same discovery, schema, and invocation routes as the console page. OpenAPI output is byte-identical to the console endpoint body.
- Scripts no longer build console URLs, tokens, and escaped routes by hand.
- No server endpoint, schema, migration, or configuration key is added. Rollback is binary-only.
- The command needs a reachable console and a saved session. It has no offline mode.
- When a session token exists, `-H Authorization` cannot override it. Module calls also strip credentials through `moduleapi`, by design.
- The HTTP client has a 10 s dial timeout, a 10 s TLS handshake timeout and a 60 s response-header timeout, and no total timeout. A module that streams forever keeps the CLI alive. This is deliberate so large responses can stream.
- Bodies over 8 MiB stay raw on a terminal.
- Agent inference costs one extra `connected-agents` request per command.
- `cmd/ssetunnel` does not expose `console`. Users of the legacy binary must use `cli`.
- The project testing policy defaults to zero new tests. Feature verification used a real stack: server, agent, and an echo module. It covered discovery, schema, OpenAPI, GET and POST invocation, header and body forwarding, agent counts, status errors, and transport errors. Focused pure tests cover the review fixes: module 401 scope, console base validation, and query-free error paths.

## Evidence

- Commit `67f8474` adds `internal/clientapp/console.go`, `internal/clicommand/console.go`, the `cmd/cli` dispatch, one `CLIUsage` line, and the AGENTS updates. Review fixes `f9fd394`, `c051e10`, and `f0a87b3` refine failure reporting, help, and flag parsing.
- `go build ./...`, `go vet ./...`, and `gofmt` pass. `go list -deps ./cmd/server` contains no client package.
- End-to-end checks confirmed JSON and OpenAPI parity, exact header and body delivery, the targeting rules, stdout and stderr separation, and exit codes 0 and 1.
- Focused tests cover the console base shape, the catalog-versus-module 401 scope, and query removal from status errors.
