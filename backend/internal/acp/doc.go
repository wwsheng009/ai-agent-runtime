// Package acp implements a subset of the Agent Client Protocol (ACP) for
// embedding aicli as an external agent over stdio.
//
// Supported agent methods:
//   - initialize
//   - session/new
//   - session/prompt
//   - session/cancel (notification)
//   - session/load (when agentCapabilities.loadSession=true and backend implements SessionLoader)
//   - session/resume (when sessionCapabilities.resume is advertised and the backend implements SessionResumer)
//   - session/list (when sessionCapabilities.list is advertised and the backend implements SessionLister)
//   - session/delete (when sessionCapabilities.delete is advertised and the backend implements SessionDeleter)
//   - session/close (when sessionCapabilities.close is advertised and the backend implements SessionCloser)
//   - session/set_config_option (select-type options need no capability; boolean options are
//     filtered out unless the client advertises clientCapabilities.session.configOptions.boolean)
//   - session/set_mode (legacy mode selector, kept until clients migrate to the "mode" config option)
//   - $/cancel_request
//
// Supported agent → client traffic:
//   - session/update (notification)
//   - session/request_permission (request/response)
//   - elicitation/create (request/response, preferred when the client advertises form-mode
//     elicitation; session/request_question stays as a fallback extension)
//
// Transport is JSON-RPC 2.0 over NDJSON (one message per line). Stdout is
// reserved for protocol messages; hosts must keep logs on stderr.
//
// This package is intentionally independent of the chat/bootstrap stack so
// protocol framing can be unit-tested with a fake SessionBackend.
package acp
