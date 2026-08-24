package wecom

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/liuzengh/trpc-agent-service/trpcservice/gateway"
)

const callbackPathPrefix = "/channels/wecom/"

// Handler verifies WeCom callbacks and hands normalized messages to the shared
// durable ingress pipeline. It never waits for Agent execution.
type Handler struct {
	bindings map[string]Binding
	acceptor gateway.InboundAcceptor
	now      func() time.Time
}

// NewHandler creates an immutable tenant binding registry.
func NewHandler(acceptor gateway.InboundAcceptor, bindings ...Binding) (*Handler, error) {
	if acceptor == nil {
		return nil, errors.New("wecom: inbound acceptor is required")
	}
	handler := &Handler{bindings: make(map[string]Binding, len(bindings)), acceptor: acceptor, now: time.Now}
	for _, binding := range bindings {
		if err := binding.validate(); err != nil {
			return nil, err
		}
		if _, exists := handler.bindings[binding.BindingID]; exists {
			return nil, fmt.Errorf("wecom: duplicate binding %q", binding.BindingID)
		}
		handler.bindings[binding.BindingID] = binding
	}
	if len(handler.bindings) == 0 {
		return nil, errors.New("wecom: at least one binding is required")
	}
	return handler, nil
}

// ServeHTTP implements GET callback verification and POST message receipt.
func (handler *Handler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	binding, ok := handler.binding(request.URL.Path)
	if !ok {
		http.NotFound(w, request)
		return
	}
	switch request.Method {
	case http.MethodGet:
		handler.verifyURL(w, request, binding)
	case http.MethodPost:
		handler.receive(w, request, binding)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (handler *Handler) binding(path string) (Binding, bool) {
	if handler == nil || !strings.HasPrefix(path, callbackPathPrefix) {
		return Binding{}, false
	}
	raw := strings.TrimPrefix(path, callbackPathPrefix)
	if raw == "" || strings.Contains(raw, "/") {
		return Binding{}, false
	}
	bindingID, err := url.PathUnescape(raw)
	if err != nil {
		return Binding{}, false
	}
	binding, ok := handler.bindings[bindingID]
	return binding, ok
}

func (handler *Handler) verifyURL(w http.ResponseWriter, request *http.Request, binding Binding) {
	query := request.URL.Query()
	plain, err := binding.Crypt.VerifyAndDecrypt(query.Get("msg_signature"), query.Get("timestamp"), query.Get("nonce"), query.Get("echostr"))
	if err != nil {
		http.Error(w, "invalid callback verification", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(plain)
}

func (handler *Handler) receive(w http.ResponseWriter, request *http.Request, binding Binding) {
	body, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
	if err != nil {
		http.Error(w, "read callback", http.StatusBadRequest)
		return
	}
	envelope, err := decodeEnvelope(body)
	if err != nil {
		http.Error(w, "invalid callback body", http.StatusBadRequest)
		return
	}
	query := request.URL.Query()
	plain, err := binding.Crypt.VerifyAndDecrypt(query.Get("msg_signature"), query.Get("timestamp"), query.Get("nonce"), envelope.Encrypt)
	if err != nil {
		http.Error(w, "invalid callback signature", http.StatusUnauthorized)
		return
	}
	message, err := decodeCallback(plain)
	if err != nil {
		http.Error(w, "invalid callback message", http.StatusBadRequest)
		return
	}
	inbound, err := normalize(binding, message, handler.now())
	if err != nil {
		if errors.Is(err, ErrUnsupportedMessage) {
			// Unsupported provider events are poison messages for the Agent path.
			// Ack them so WeCom does not retry them three times.
			writeSuccess(w)
			return
		}
		if errors.Is(err, ErrBindingMismatch) {
			http.Error(w, "callback binding mismatch", http.StatusUnauthorized)
			return
		}
		http.Error(w, "invalid callback message", http.StatusBadRequest)
		return
	}
	if _, err := handler.acceptor.AcceptInbound(request.Context(), inbound); err != nil {
		// A non-200 response asks WeCom to retry a temporary Inbox/queue failure.
		http.Error(w, "temporarily unable to accept callback", http.StatusServiceUnavailable)
		return
	}
	writeSuccess(w)
}

func writeSuccess(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("success"))
}
