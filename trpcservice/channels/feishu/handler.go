package feishu

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway"
)

const callbackPathPrefix = "/channels/feishu/"

// BindingProvider resolves the enabled callback bindings for one binding_id
// from the control plane at request time. Multiple tenants may share a
// binding_id, so it returns every candidate; the handler disambiguates with
// the server-owned Verification Token.
type BindingProvider func(bindingID string) []Binding

// Handler verifies Feishu event-subscription callbacks and hands normalized
// messages to the shared durable ingress pipeline. It acknowledges quickly
// and never waits for Agent execution.
type Handler struct {
	provider BindingProvider
	acceptor gateway.InboundAcceptor
	now      func() time.Time
}

// NewDynamicHandler resolves bindings per callback from the control plane.
func NewDynamicHandler(acceptor gateway.InboundAcceptor, provider BindingProvider) (*Handler, error) {
	if acceptor == nil || provider == nil {
		return nil, errors.New("feishu: inbound acceptor and binding provider are required")
	}
	return &Handler{provider: provider, acceptor: acceptor, now: time.Now}, nil
}

// ServeHTTP implements the Feishu callback endpoint. Feishu only uses POST:
// URL verification and events share the same path.
func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	candidates, ok := handler.bindings(request.URL.Path)
	if !ok {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeError(writer, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1<<20))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid callback body")
		return
	}
	var raw struct {
		Encrypt string `json:"encrypt"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid callback body")
		return
	}
	payload := body
	encrypted := strings.TrimSpace(raw.Encrypt) != ""
	if encrypted {
		payload, ok = handler.decryptCandidates(candidates, raw.Encrypt)
		if !ok {
			writeError(writer, http.StatusUnauthorized, "invalid encrypted callback")
			return
		}
	} else {
		candidates = plaintextCandidates(candidates)
		if len(candidates) == 0 {
			// Encryption is enabled for every candidate binding: never fall
			// back to plaintext silently.
			writeError(writer, http.StatusUnauthorized, "plaintext callback is not accepted")
			return
		}
	}
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid callback body")
		return
	}
	if probe.Type == "url_verification" {
		handler.verifyURL(writer, candidates, payload)
		return
	}
	handler.receive(writer, request, candidates, payload)
}

// bindings extracts the binding_id from the URL path. It only selects
// server-side control-plane configuration; tenant and app scope can never be
// supplied by the client.
func (handler *Handler) bindings(path string) ([]Binding, bool) {
	if handler == nil || handler.provider == nil || !strings.HasPrefix(path, callbackPathPrefix) {
		return nil, false
	}
	raw := strings.TrimPrefix(path, callbackPathPrefix)
	if raw == "" || strings.Contains(raw, "/") {
		return nil, false
	}
	bindingID, err := url.PathUnescape(raw)
	if err != nil || bindingID == "" {
		return nil, false
	}
	candidates := handler.provider(bindingID)
	if len(candidates) == 0 {
		return nil, false
	}
	return candidates, true
}

// decryptCandidates tries every candidate's Encrypt Key and returns the
// plaintext plus the candidates that could actually decrypt the payload.
func (handler *Handler) decryptCandidates(candidates []Binding, encoded string) ([]byte, bool) {
	for _, binding := range candidates {
		if binding.EncryptKey == "" {
			continue
		}
		plain, err := decrypt(binding.EncryptKey, encoded)
		if err == nil {
			return plain, true
		}
	}
	return nil, false
}

// plaintextCandidates keeps only bindings without an Encrypt Key.
func plaintextCandidates(candidates []Binding) []Binding {
	var plain []Binding
	for _, binding := range candidates {
		if binding.EncryptKey == "" {
			plain = append(plain, binding)
		}
	}
	return plain
}

// verifyURL answers the URL verification handshake with the challenge when
// one candidate's Verification Token matches.
func (handler *Handler) verifyURL(writer http.ResponseWriter, candidates []Binding, payload []byte) {
	var verification urlVerification
	if err := json.Unmarshal(payload, &verification); err != nil || strings.TrimSpace(verification.Challenge) == "" {
		writeError(writer, http.StatusBadRequest, "invalid verification body")
		return
	}
	if matchCandidate(candidates, verification.Token) == nil {
		writeError(writer, http.StatusUnauthorized, "invalid verification token")
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(map[string]string{"challenge": verification.Challenge})
}

// receive validates one v2 event and hands it to the durable ingress.
func (handler *Handler) receive(writer http.ResponseWriter, request *http.Request, candidates []Binding, payload []byte) {
	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid callback body")
		return
	}
	binding := matchCandidate(candidates, env.Header.Token)
	if binding == nil {
		writeError(writer, http.StatusUnauthorized, "invalid verification token")
		return
	}
	inbound, err := normalize(*binding, env, handler.now())
	if err != nil {
		switch {
		case errors.Is(err, ErrUnsupportedMessage):
			// Unsupported provider events are poison messages for the Agent
			// path. Ack them so Feishu does not retry.
			writeAccepted(writer)
		case errors.Is(err, ErrBindingMismatch):
			writeError(writer, http.StatusUnauthorized, "callback binding mismatch")
		default:
			writeError(writer, http.StatusBadRequest, "invalid callback message")
		}
		return
	}
	if _, err := handler.acceptor.AcceptInbound(request.Context(), inbound); err != nil {
		// A 5xx response asks Feishu to retry a temporary Inbox/queue failure.
		writeError(writer, http.StatusServiceUnavailable, "temporarily unable to accept callback")
		return
	}
	writeAccepted(writer)
}

// matchCandidate picks the candidate whose Verification Token matches using
// constant-time comparison. This is what separates tenants that share a
// binding_id, and what rejects forged callbacks.
func matchCandidate(candidates []Binding, token string) *Binding {
	if token == "" {
		return nil
	}
	for i := range candidates {
		expected := candidates[i].VerificationToken
		if expected != "" && len(expected) == len(token) && subtle.ConstantTimeCompare([]byte(expected), []byte(token)) == 1 {
			return &candidates[i]
		}
	}
	return nil
}

func writeAccepted(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(`{"code":0}`))
}

// writeError never includes secrets, encrypted payloads, or user content.
func writeError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"error": message})
}
