// Package sessionmemoryhttp implements the vendor-neutral session-memory
// Provider HTTP/JSON v1 protocol.
//
// The adapter depends only on the extraction-ready sessionmemory contracts. It
// deliberately has no knowledge of Balda transports, sessions, JetStream, or
// MCP. Provider credentials are sent as request headers and never appear in
// returned diagnostics.
package sessionmemoryhttp
