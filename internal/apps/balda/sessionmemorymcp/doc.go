// Package sessionmemorymcp exposes the Balda-owned authentication bridge for
// the neutral session-memory MCP adapter. Tool registration lives in the
// public sessionmemory/mcp package; this package binds one broker capability
// to one exact locator/session and keeps that context out of tool arguments.
//
// The public tools accept bounded search/trace arguments, while the current
// Balda locator and session identity are supplied by a server-side resolver,
// never by tool arguments. Recalled text is returned as untrusted reference
// data and is not injected into a prompt or executed by this package.
package sessionmemorymcp
