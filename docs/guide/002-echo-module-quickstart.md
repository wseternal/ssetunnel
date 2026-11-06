# Echo Module — Quick-Start Guide

Build your first ssetunnel agent module in 6 steps. This guide walks you through the echo module — a simple service that echoes back every HTTP request as JSON.

## Prerequisites

- Go 1.26 or later
- An ssetunnel server and agent running (see the project README for setup)
- Basic familiarity with Go HTTP handlers

## Step 1: Create the Project

Create a new directory for the module and initialize a Go module:

```bash
mkdir echo && cd echo
go mod init github.com/yourname/echo
```

Add the ssetunnel SDK as a dependency:

```bash
go get github.com/wseternal/ssetunnel/sdk@latest
```

Your `go.mod` should include:

```
require github.com/wseternal/ssetunnel v0.0.0
```

> **Tip:** If you are developing against a local checkout, add a replace directive:
> ```
> replace github.com/wseternal/ssetunnel => ../path/to/ssetunnel
> ```

## Step 2: Write the OpenAPI Spec

Create `openapi.yaml` describing the echo endpoint:

```yaml
openapi: "3.0.3"
info:
  title: Echo Module
  description: Echoes back request details as JSON.
  version: "0.1.0"
paths:
  /echo:
    get:
      operationId: echoGet
      summary: Echo query parameters
      responses:
        "200":
          description: Echoed request details
    post:
      operationId: echoPost
      summary: Echo request body
      responses:
        "200":
          description: Echoed request details
  /health:
    get:
      operationId: healthCheck
      summary: Health check
      responses:
        "200":
          description: Module is healthy
```

The agent uses this spec to validate incoming requests before forwarding them to the module.

## Step 3: Implement the Echo Handler

Create `main.go`:

```go
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/wseternal/ssetunnel/sdk"
)

//go:embed openapi.yaml
var openapiYAML []byte

type echoResponse struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query,omitempty"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body,omitempty"`
}

func echoHandler(w http.ResponseWriter, r *http.Request) {
	resp := echoResponse{
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   r.URL.RawQuery,
		Headers: r.Header,
	}
	if r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err == nil && len(body) > 0 {
			resp.Body = string(body)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"healthy"}`))
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	m := sdk.Manifest{
		ModuleID:       "com.example.echo",
		ServiceName:    "echo",
		ServiceVersion: "0.1.0",
		OpenAPIYAML:    openapiYAML,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/echo", echoHandler)
	mux.HandleFunc("/health", healthHandler)

	log.Printf("echo: starting module %s", m.ModuleID)
	if err := sdk.Serve(ctx, m, mux); err != nil {
		log.Fatalf("echo: %v", err)
	}
}
```

**Key concepts:**

- **`//go:embed openapi.yaml`** embeds the spec into the binary. No separate file needed at runtime.
- **`sdk.Manifest`** declares the module identity. The module ID uses reverse-DNS format.
- **`sdk.Serve(ctx, manifest, handler)`** handles IPC registration, health reporting, and request routing. The function blocks until the context is cancelled.
- **Standard `http.Handler`** — the module implements a normal HTTP handler. The SDK bridges between the agent's IPC invocation socket and the handler.

## Step 4: Build the Binary

```bash
go build -o echo .
```

Verify the binary was created:

```bash
ls -la echo
```

## Step 5: Register with the Agent

The agent manages module processes through its supervisor. You cannot run a module standalone — the agent creates the IPC socket and generates the credential that the module needs.

| Variable | Purpose | Set by |
|----------|---------|--------|
| `SSETUNNEL_MODULE_IPC_ENDPOINT` | Path to the IPC unix socket | Agent |
| `SSETUNNEL_MODULE_IPC_CREDENTIAL` | Random credential for authenticating | Agent |

**The workflow:**

1. Start the agent — it creates an IPC socket at `<dataDir>/module.sock` and a random credential
2. Install the echo module package through the console API
3. Enable the module — the supervisor spawns the echo process with the correct env vars
4. The module connects, authenticates, registers, and starts serving

```bash
# 1. Start the agent (creates the IPC socket).
./ssetunnel agent --server-url http://localhost:8080 --token YOUR_TOKEN

# 2. The supervisor manages the module lifecycle automatically.
#    On agent restart, enabled modules are restored from desired_state.json.
```

**What happens when the supervisor starts the module:**
1. The supervisor spawns the echo binary as a child process
2. Sets `SSETUNNEL_MODULE_IPC_ENDPOINT` and `SSETUNNEL_MODULE_IPC_CREDENTIAL` in the child's environment
3. The module connects to the IPC endpoint and authenticates
4. The module registers itself as `com.example.echo`
5. The module starts serving HTTP requests on the invocation socket
6. Health is reported every 30 seconds

**Crash recovery:** If the module crashes, the supervisor restarts it with jittered exponential backoff (500 ms base, 30 s cap).

## Step 6: Test from the Console

1. Open the admin console in your browser
2. Navigate to the **Modules** tab
3. Select the agent running the echo module
4. Click the **echo** module card
5. The **Try It** panel appears above the OpenAPI spec:
   - Select a method (GET or POST)
   - Set the path to `/echo`
   - Add headers (e.g., `Content-Type: application/json`)
   - Type a JSON body for POST requests
   - Click **Send**
6. The response appears below with the echoed method, path, headers, and body

**Expected response for POST /echo:**

```json
{
  "method": "POST",
  "path": "/echo",
  "headers": {
    "Content-Type": ["application/json"],
    "X-Correlation-Id": ["corr-abc123"]
  },
  "body": "{\"message\": \"Hello from the console!\"}"
}
```

## What Happens Under the Hood

```
Console UI
  │  POST /api/v1/modules/invoke/{agent}/{module}/echo
  ▼
Server (module_routes.go)
  │  Auth check + OpenAPI validation + send module_open control message
  ▼
Agent (module_invoke.go)
  │  Open reverse yamux stream + dial IPC invocation socket
  ▼
Echo Module (your binary)
  │  http.Handler receives the request, writes JSON response
  ▼
Response flows back through: IPC → yamux stream → server → console
```

## Next Steps

- Add more endpoints to the OpenAPI spec and handler
- Use the `sdk.Health` type to report degraded status from your handler
- Add UI assets with `sdk.UIDescriptor` for rich console integration
- Package the module for distribution (see the packaging guide)
