// Package sdk provides the public Go SDK for the ssetunnel agent module
// registry. It defines stable types for module manifests, discovery,
// invocation, streaming, cancellation, and typed errors.
//
// The SDK hides local IPC and tunnel framing from module authors. Providers
// supply an OpenAPI v3 YAML document and a standard http.Handler. Consumers
// use agent, module, epoch, and revision references to discover and invoke
// HTTP operations through the tunnel.
//
// # Providers
//
// A provider implements a module by registering a manifest, serving an
// OpenAPI-described HTTP handler, and optionally declaring UI assets:
//
//	m := sdk.Manifest{
//	    ModuleID:      "com.example.mymodule",
//	    ServiceName:   "my-service",
//	    ServiceVersion: semver.MustParse("1.0.0"),
//	    OpenAPIYAML:   openapiBytes,
//	    Handler:       myHTTPHandler,
//	}
//	err := sdk.Serve(ctx, m)
//
// # Consumers
//
// A consumer discovers modules for a selected agent and invokes operations
// through the tunnel:
//
//	client := sdk.NewConsumer(serverURL, authToken)
//	catalog, err := client.Discover(ctx, agentID)
//	resp, err := client.Invoke(ctx, ref, req)
//
// # Errors
//
// The SDK uses RFC 7807 problem details for transport and registry failures.
// Callers inspect [ModuleError] to determine the failure category and act
// accordingly:
//
//	var merr *sdk.ModuleError
//	if errors.As(err, &merr) {
//	    switch merr.Type {
//	    case sdk.ErrTypeTransport:  // network or tunnel failure
//	    case sdk.ErrTypeRegistry:   // module not found, stale revision
//	    case sdk.ErrTypeModule:     // module returned an error response
//	    }
//	}
//
// The SDK exposes no yamux, SSE, IPC, or stream identifiers in its public
// API. Internal transport details remain private.
package sdk
