package events

// EventAssistantStreamReset marks a retry boundary for one logical LLM request.
//
// A provider retry may regenerate the complete response after the previous
// attempt already streamed user-visible text. The next attempt reuses the same
// llm_request_id/stream_id, so renderers cannot infer the boundary from stream
// identity or sequence numbers alone. Consumers must discard any non-terminal
// assistant/reasoning cells for the carried request identity before applying
// the next attempt's deltas.
const EventAssistantStreamReset = "assistant_stream_reset"
