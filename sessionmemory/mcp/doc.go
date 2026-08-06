// Package mcp exposes the transport-neutral session-memory MCP surface.
//
// It registers only session_memory.search and session_memory.trace. Exact
// scope is supplied by an injected authenticated resolver; locator formats,
// headers, capability brokers, and provider policy remain host-owned. Search
// and trace results are bounded untrusted canonical references.
package mcp
