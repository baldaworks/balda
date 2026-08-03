// Package sessionmemorymcp exposes locator-bound session-memory recall through
// a dedicated MCP tool.
//
// The tool accepts only a query and an optional bounded result limit. The
// current Balda locator and session identity are supplied by a server-side
// resolver, never by tool arguments. Recalled text is returned as untrusted
// reference data and is not injected into a prompt or executed by this
// package.
package sessionmemorymcp
