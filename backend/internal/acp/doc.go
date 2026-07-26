// Package acp implements a subset of the Agent Client Protocol (ACP) for
// embedding aicli as an external agent over stdio.
//
// Supported agent methods:
//   - initialize
//   - session/new
//   - session/prompt
//   - session/cancel (notification)
//
// Supported agent → client traffic:
//   - session/update (notification)
//   - session/request_permission (request/response)
//
// Transport is JSON-RPC 2.0 over NDJSON (one message per line). Stdout is
// reserved for protocol messages; hosts must keep logs on stderr.
//
// This package is intentionally independent of the chat/bootstrap stack so
// protocol framing can be unit-tested with a fake SessionBackend.
package acp
