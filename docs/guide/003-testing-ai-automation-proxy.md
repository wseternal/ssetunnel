# Testing Guide: AI Automation Proxy & Data Store Framework

This guide covers end-to-end testing of the AI automation proxy module and the generic data store framework.

---

## Prerequisites

1. **PostgreSQL** running locally (or via Docker)
2. **Go 1.22+** installed
3. **Node.js 18+** and **bun** for the WXT extension
4. **Chrome/Chromium** browser for extension testing

---

## Part 1: Infrastructure Setup

### 1.1 Start PostgreSQL

```bash
# Option A: Docker
docker run -d --name ssetunnel-pg \
  -e POSTGRES_USER=ssetunnel \
  -e POSTGRES_PASSWORD=ssetunnel \
  -e POSTGRES_DB=ssetunnel \
  -p 5432:5432 \
  postgres:16

# Option B: Local PostgreSQL
# Ensure postgres is running and create the database:
createdb ssetunnel
```

### 1.2 Run Migrations

```bash
cd /Users/jiangzhaohua/codes/wseternal/ssetunnel

# Set database URL
export DATABASE_URL="postgres://ssetunnel:ssetunnel@localhost:5432/ssetunnel?sslmode=disable"

# Run atlas migrations (or use your preferred migration tool)
atlas migrate apply --url "$DATABASE_URL" --dir "file://migrations"
```

### 1.3 Build All Binaries

```bash
cd /Users/jiangzhaohua/codes/wseternal/ssetunnel

# Build main binaries
go build -o ./build/ssetunnel ./cmd/ssetunnel
go build -o ./build/server ./cmd/server
go build -o ./build/cli ./cmd/cli

# Build the AI automation proxy module
go build -o ./build/ai-automation-proxy ./cmd/modules/ai-automation-proxy
```

To build and install every builtin module in one step, run `task modules:install`.

---

## Part 2: Server & Agent Setup

### 2.1 Start the Tunnel Server

```bash
cd /Users/jiangzhaohua/codes/wseternal/ssetunnel

export DATABASE_URL="postgres://ssetunnel:ssetunnel@localhost:5432/ssetunnel?sslmode=disable"

# Start server with auth disabled for easier testing
./local.sh server --disable-auth --port :8080 --console-port :8081
```

The server runs on:
- `:8080` — tunnel endpoints
- `:8081` — admin console

### 2.2 Create a Test Token (if auth enabled)

```bash
# If running with auth, create a token via the console API:
curl -X POST http://localhost:8081/api/v1/tokens \
  -H "Content-Type: application/json" \
  -d '{"description": "test token", "role": "admin"}'
```

### 2.3 Start the Agent

```bash
cd /Users/jiangzhaohua/codes/wseternal/ssetunnel

# Start agent pointing to local server
./local.sh agent \
  --server http://localhost:8080 \
  --agent-id test-agent \
  --target 127.0.0.1:22
```

The agent will:
- Connect to the server
- Create an IPC endpoint for modules
- Store module state in `~/.ssetunnel/agent`

---

## Part 3: Test the Data Store Framework

### 3.1 Verify Data Store via SDK (Unit Test)

Create a test program to verify the SDK DataStore API:

```bash
cat > /tmp/test-datastore.go << 'EOF'
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/wseternal/ssetunnel/sdk"
)

func main() {
	// Set environment variables (from agent's IPC endpoint)
	os.Setenv("SSETUNNEL_MODULE_IPC_ENDPOINT", os.ExpandEnv("$HOME/.ssetunnel/agent/module.sock"))
	os.Setenv("SSETUNNEL_MODULE_IPC_CREDENTIAL", "YOUR_CREDENTIAL_HERE")

	ds, err := sdk.NewDataStore()
	if err != nil {
		log.Fatalf("NewDataStore: %v", err)
	}
	defer ds.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Test Create
	data := json.RawMessage(`{"message": "hello from test"}`)
	err = ds.Create(ctx, "com.test.module", "record-1", data)
	if err != nil {
		log.Fatalf("Create: %v", err)
	}
	fmt.Println("✓ Create succeeded")

	// Test Get
	record, err := ds.Get(ctx, "com.test.module", "record-1")
	if err != nil {
		log.Fatalf("Get: %v", err)
	}
	fmt.Printf("✓ Get succeeded: %+v\n", record)

	// Test List
	records, err := ds.List(ctx, "com.test.module", nil)
	if err != nil {
		log.Fatalf("List: %v", err)
	}
	fmt.Printf("✓ List succeeded: %d records\n", len(records))

	// Test Delete
	err = ds.Delete(ctx, "com.test.module", "record-1")
	if err != nil {
		log.Fatalf("Delete: %v", err)
	}
	fmt.Println("✓ Delete succeeded")
}
EOF
```

### 3.2 Get the IPC Credential

```bash
# The credential is generated when the agent starts and logged.
# Look for this line in agent logs:
#   agent: module IPC credential: abcdef12...

# Or read from the agent's data directory:
cat ~/.ssetunnel/agent/credential 2>/dev/null || echo "Check agent logs for credential"
```

### 3.3 Run the Test

```bash
cd /Users/jiangzhaohua/codes/wseternal/ssetunnel
go run /tmp/test-datastore.go
```

**Expected output:**
```
✓ Create succeeded
✓ Get succeeded: &{ModuleID:com.test.module RecordID:record-1 Data:{"message": "hello from test"} ...}
✓ List succeeded: 1 records
✓ Delete succeeded
```

---

## Part 4: Test the AI Automation Proxy Module

### 4.1 Install the Module

```bash
# Build and install every builtin module. Each module lands in its own
# directory under ~/.ssetunnel/modules, with the binary and openapi.yaml:
#   ~/.ssetunnel/modules/ai-automation-proxy/ai-automation-proxy
#   ~/.ssetunnel/modules/ai-automation-proxy/openapi.yaml
task modules:install

# The agent scans ~/.ssetunnel/modules at startup and enables every
# package it finds. Restart the agent, or reload it with SIGHUP:
./build/cli agent restart
```

### 4.2 Start the Module Manually

```bash
# Set environment variables for the module
export SSETUNNEL_MODULE_IPC_ENDPOINT="$HOME/.ssetunnel/agent/module.sock"
export SSETUNNEL_MODULE_IPC_CREDENTIAL="<credential_from_step_3.2>"

# Start the module
./build/ai-automation-proxy --listen :9090 --job-timeout 5m
```

**Expected output:**
```
aiproxy: starting module com.wseternal.ai-automation-proxy (extension server on :9090)
aiproxy: extension server listening on :9090
provider: registered module com.wseternal.ai-automation-proxy
```

### 4.3 Verify Module Registration

```bash
# Check that the module appears in the agent's catalog:
curl -s http://localhost:8081/api/v1/modules?agent=test-agent | jq .
```

**Expected output:**
```json
{
  "modules": [
    {
      "module_id": "com.wseternal.ai-automation-proxy",
      "service_name": "ai-automation-proxy",
      "service_version": "0.1.0",
      "health": "healthy"
    }
  ]
}
```

### 4.4 Test Job Submission (Client API)

A submit succeeds only while an automation client is connected. If no extension polls
the module, the module answers `503 NO_AUTOMATION_CLIENT`. A queued job would never run
and would end at its own timeout. Set up the extension first (Part 5.1 to 5.4) when you
test a submit here. `GET /health` reports the same state as `extension_connected`. That
field follows the submit rule exactly: a client counts for 90 s after its last poll or
result, and any running job counts as connected, so the field can stay true briefly after
the extension disappears. Check it before you submit:

```bash
curl -s http://localhost:8081/api/v1/modules/invoke/test-agent/com.wseternal.ai-automation-proxy/health | jq .
# {"status": "healthy", "extension_connected": false}   # true after the extension connects
```

```bash
# Submit a job through the tunnel (invoke the module):
curl -X POST http://localhost:8081/api/v1/modules/invoke/test-agent/com.wseternal.ai-automation-proxy/jobs \
  -H "Content-Type: application/json" \
  -d '{
    "prompt": "What is the capital of France?",
    "provider_id": "gemini"
  }'
```

**Expected output:**
```json
{"job_id": "abc123def456..."}
```

#### Submit a job with a file

Attachment bytes go to the server object store, so the server must run with a storage root:

```bash
./local.sh server --disable-auth --object-store-dir /tmp/ssetunnel-objects
```

Then send the file as base64. The limit is 4 MiB of decoded bytes, and `byte_length` is
required: it must equal the decoded size.

```bash
BASE64=$(base64 -i /tmp/report.txt | tr -d '\n')
curl -X POST http://localhost:8081/api/v1/modules/invoke/test-agent/com.wseternal.ai-automation-proxy/jobs \
  -H "Content-Type: application/json" \
  -d "{\"prompt\": \"summarise this\", \"file\": {\"name\": \"report.txt\", \"mime_type\": \"text/plain\", \"data_base64\": \"$BASE64\", \"byte_length\": $(wc -c < /tmp/report.txt)}}"
```

The module gzips the bytes when that shrinks them and stores them under a content-addressed
key. `GET /jobs/{id}` then shows `file.object_key`, `file.sha256`, and `file.byte_length`
instead of `file.data_base64`. Without `--object-store-dir`, the submit returns
`503 FILE_STORE_UNAVAILABLE`. A job whose stored attachment cannot be read later fails with
`FILE_UNAVAILABLE`; the two codes describe different conditions.

### 4.5 Poll for Job Status

```bash
JOB_ID="<job_id_from_above>"

# Check status (will be "pending" until extension picks it up):
curl -s "http://localhost:8081/api/v1/modules/invoke/test-agent/com.wseternal.ai-automation-proxy/jobs/${JOB_ID}" | jq .

# Or use long-polling (wait up to 20s for completion):
curl -s "http://localhost:8081/api/v1/modules/invoke/test-agent/com.wseternal.ai-automation-proxy/jobs/${JOB_ID}?wait=true" | jq .
```

The module caps a wait at 20 s. The agent aborts a module exchange after 30 s. A longer
wait would end before the module can answer, so the module clamps `wait_timeout` values
above the cap. A job that is still pending while no extension is connected returns at
once and does not hold the connection.

**Expected output (before extension runs):**
```json
{
  "id": "abc123...",
  "status": "pending",
  "request": {"prompt": "What is the capital of France?", ...},
  "result": null,
  "created_at": "2026-11-16T...",
  "updated_at": "2026-11-16T..."
}
```

---

## Part 5: Test the WXT Extension Integration

### 5.1 Build the Extension

```bash
cd /Users/jiangzhaohua/wseternal/gemini-automation-extension

# Install dependencies
bun install

# Build for production
bun run build
```

### 5.2 Load the Extension in Chrome

1. Open `chrome://extensions/`
2. Enable "Developer mode"
3. Click "Load unpacked"
4. Select `/Users/jiangzhaohua/wseternal/gemini-automation-extension/.output/chrome-mv3-prod`

### 5.3 Configure the Long-Polling Client

Open the extension's popup or use the console to set the config:

```javascript
// In Chrome DevTools Console (on extension's background page):
chrome.storage.local.set({
  'llm-automation/long-poll-config': {
    proxyUrl: 'http://localhost:9090',
    enabled: true
  }
});
```

Or via the extension's popup UI (if you add a settings page).

### 5.4 Verify Extension Connection

Check the extension's background page console:
1. Go to `chrome://extensions/`
2. Click "Service Worker" link for the extension
3. Look for: `[long-poll] starting client: http://localhost:9090`

### 5.5 End-to-End Test

1. **Submit a job** (from Part 4.4):
   ```bash
   curl -X POST http://localhost:8081/api/v1/modules/invoke/test-agent/com.wseternal.ai-automation-proxy/jobs \
     -H "Content-Type: application/json" \
     -d '{"prompt": "Explain quantum computing in simple terms", "provider_id": "gemini"}'
   ```

2. **Watch the extension console** — you should see:
   ```
   [long-poll] received job: {job_id: "...", request: {...}}
   ```

3. **The extension opens a Gemini tab**, fills the prompt, submits, and extracts the response.

4. **Poll for the result**:
   ```bash
   curl -s "http://localhost:8081/api/v1/modules/invoke/test-agent/com.wseternal.ai-automation-proxy/jobs/${JOB_ID}?wait=true" | jq .
   ```

5. **Expected final output:**
   ```json
   {
     "id": "...",
     "status": "completed",
     "result": {
       "status": "ok",
       "text": "Quantum computing is a type of computing...",
       "meta": {"tabId": 123, "providerId": "gemini", "durationMs": 15000}
     }
   }
   ```

---

## Part 6: Troubleshooting

### Module Not Appearing in Catalog

```bash
# Check agent logs for module registration:
tail -f ~/.ssetunnel/agent/logs/agent.log | grep -i module

# Verify IPC endpoint exists:
ls -la ~/.ssetunnel/agent/module.sock
```

### Data Store Requests Failing

```bash
# Check server logs for data store errors:
tail -f /tmp/ssetunnel-server/logs/server.log | grep -i datastore

# Verify database connection:
psql "$DATABASE_URL" -c "SELECT * FROM module_records LIMIT 5;"
```

### Extension Not Connecting to Proxy

```bash
# Verify proxy is listening:
curl -s http://localhost:9090/health
# Expected: {"status":"healthy"}

# Check extension console for errors
# (Service Worker → Console tab)

# Verify host permissions:
# chrome://extensions → LLM Automation Bridge → Details → Permissions
# Should include: http://localhost:*
```

### Job Stuck in "pending"

- The extension's long-poll client may not be running
- Check extension storage: `chrome.storage.local.get('llm-automation/long-poll-config')`
- Ensure `enabled: true` and `proxyUrl` is correct

### Job Times Out

- LLM automation can take 30-120 seconds depending on response length
- Increase `--job-timeout` when starting the proxy
- Check if Gemini/DeepSeek is rate-limiting or requiring CAPTCHA

---

## Part 7: Cleanup

```bash
# Stop the proxy module (Ctrl+C)
# Stop the agent (Ctrl+C)
# Stop the server (Ctrl+C)

# Remove test data:
psql "$DATABASE_URL" -c "DELETE FROM module_records WHERE module_id = 'com.test.module';"

# Remove agent state:
rm -rf ~/.ssetunnel/agent

# Stop PostgreSQL (if Docker):
docker stop ssetunnel-pg
docker rm ssetunnel-pg
```

---

## Quick Reference

| Endpoint | URL | Purpose |
|----------|-----|---------|
| Tunnel API | `http://localhost:8080` | Agent transport |
| Console API | `http://localhost:8081/api/v1` | Management API |
| Module Invoke | `http://localhost:8081/api/v1/modules/invoke/<agent>/<module><path>` | Invoke modules |
| Proxy Extension | `http://localhost:9090/poll` | Extension long-poll |
| Proxy Result | `http://localhost:9090/result` | Extension result POST |

---

## Test Checklist

- [ ] PostgreSQL running and migrated
- [ ] Server started with `--disable-auth`
- [ ] Agent connected and registered
- [ ] AI proxy module started and registered in catalog
- [ ] Data Store SDK test passed (Create/Get/List/Delete)
- [ ] Job submission via module invoke succeeded
- [ ] WXT extension built and loaded
- [ ] Long-poll client configured and connected
- [ ] End-to-end job completed (submit → extension → result)
- [ ] Job result visible via poll endpoint
