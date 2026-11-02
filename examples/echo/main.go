// Package main implements the echo module — a minimal example for the
// ssetunnel agent module registry. It echoes back every HTTP request as a
// JSON object containing the method, path, headers, and body.
//
// Usage:
//
//	SSETUNNEL_MODULE_IPC_ENDPOINT=/tmp/ssetunnel-mod.sock \
//	SSETUNNEL_MODULE_IPC_CREDENTIAL=$(cat /tmp/ssetunnel-mod.cred) \
//	./echo
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

// echoResponse is the JSON structure returned for every echo request.
type echoResponse struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query,omitempty"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body,omitempty"`
}

// echoHandler serves GET and POST /echo by reading the request and writing
// the details back as JSON.
func echoHandler(w http.ResponseWriter, r *http.Request) {
	resp := echoResponse{
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   r.URL.RawQuery,
		Headers: r.Header,
	}

	// Read the body (up to 1 MiB to prevent abuse).
	if r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err == nil && len(body) > 0 {
			resp.Body = string(body)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

// healthHandler returns a simple health status.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"healthy"}`))
}

func main() {
	log.SetFlags(log.Ltime | log.Lshortfile)

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
