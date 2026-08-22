// Package gateway defines transport-neutral message contracts.
package gateway

import (
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/tenant"
)

// InboundMessage is a verified and tenant-scoped external user message.
type InboundMessage struct {
	TenantID, AppID, BindingID string
	ExternalMessageID          string
	ExternalUserID             string
	UserID, SessionID          string
	Text                       string
	TraceID                    string
	ConfigVersion              tenant.ConfigVersion
	ReceivedAt                 time.Time
	TraceContext               map[string]string
}

// RunRequest is the durable unit dispatched to an Agent Worker.
type RunRequest struct {
	InboxID                    string
	TenantID, AppID, BindingID string
	ExternalMessageID          string
	UserID, SessionID, Text    string
	TraceID                    string
	ConfigVersion              tenant.ConfigVersion
	ClaimOwner, ClaimToken     string
	ClaimAttempt               int
	InboxSeq                   uint64
	ClaimLeaseUntil            time.Time
	TraceContext               map[string]string
}

// OutboundMessage is a durable reply awaiting channel delivery.
type OutboundMessage struct {
	TenantID, AppID, BindingID   string
	OutboxID, DedupeKey          string
	UserID, SessionID            string
	Text, TraceID                string
	SourceInboxID, SourceEventID string
	CreatedAt                    time.Time
}

// SessionKey is the complete tenant-scoped session identity.
type SessionKey struct{ TenantID, AppID, UserID, SessionID string }

// Key returns the session identity carried by request.
func (request RunRequest) Key() SessionKey {
	return SessionKey{request.TenantID, request.AppID, request.UserID, request.SessionID}
}

// RunEvent is a transport-neutral projection of an Agent event.
type RunEvent struct {
	Type, RequestID, TraceID string
	SessionID                string
	Delta, Message, ToolName string
	Stage, ToolStatus        string
	ToolCallID               string
	Error                    string
	Terminal                 bool
	PromptTokens             int
	CompletionTokens         int
	TotalTokens              int
}

// EventPublisher observes execution without owning persistence or routing.
type EventPublisher interface {
	Publish(RunEvent)
}

// EventPublisherFunc adapts a function to EventPublisher.
type EventPublisherFunc func(RunEvent)

// Publish implements EventPublisher.
func (publish EventPublisherFunc) Publish(event RunEvent) { publish(event) }
