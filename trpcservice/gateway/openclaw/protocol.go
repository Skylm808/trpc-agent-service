// Package openclaw exposes an OpenClaw-compatible HTTP message surface.
package openclaw

import "encoding/json"

// ContentPart is one text, image, or file input. PR5 executes text parts and preserves the DTO for adapters.
type ContentPart struct {
	Type     string `json:"type,omitempty"`
	Text     string `json:"text,omitempty"`
	URL      string `json:"url,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

// MessageRequest is the stable external gateway request shape.
type MessageRequest struct {
	Channel        string         `json:"channel,omitempty"`
	From           string         `json:"from,omitempty"`
	To             string         `json:"to,omitempty"`
	ConversationID string         `json:"conversation_id,omitempty"`
	ThreadID       string         `json:"thread_id,omitempty"`
	MessageID      string         `json:"message_id"`
	Text           string         `json:"text,omitempty"`
	ContentParts   []ContentPart  `json:"content_parts,omitempty"`
	UserID         string         `json:"user_id,omitempty"`
	SessionID      string         `json:"session_id,omitempty"`
	RequestID      string         `json:"request_id,omitempty"`
	Model          string         `json:"model,omitempty"`
	Extensions     map[string]any `json:"extensions,omitempty"`
}

// MessageResponse acknowledges durable acceptance without waiting for the Agent.
type MessageResponse struct {
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	TraceID   string `json:"trace_id"`
	Accepted  bool   `json:"accepted"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

// StreamEvent is serialized as one SSE data record.
type StreamEvent struct {
	Type       string          `json:"type"`
	RequestID  string          `json:"request_id"`
	TraceID    string          `json:"trace_id"`
	Delta      string          `json:"delta,omitempty"`
	Message    string          `json:"message,omitempty"`
	ToolName   string          `json:"tool_name,omitempty"`
	Error      string          `json:"error,omitempty"`
	Terminal   bool            `json:"terminal,omitempty"`
	Extensions json.RawMessage `json:"extensions,omitempty"`
}
