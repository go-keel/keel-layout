// Example file: delete the whole file after forking

package service

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-keel/keel-layout/internal/conf"
	"github.com/go-keel/keel/core/kconfig"
	"github.com/go-keel/keel/middleware/metadata"
)

// WebhookHandler shows the raw HTTP path: a callback from an external system needs the raw body for HMAC
// verification, which does not fit a proto contract, so it registers an http.HandlerFunc directly (bypassing the
// unary middleware chain).
type WebhookHandler struct {
	bootstrap *kconfig.Dynamic[conf.Bootstrap]
}

func NewWebhookHandler(bootstrap *kconfig.Dynamic[conf.Bootstrap]) *WebhookHandler {
	return &WebhookHandler{bootstrap: bootstrap}
}

// maxWebhookBody is the callback request body limit. A raw handler bypasses the middleware chain and nobody caps
// the body size for it, so it must set one itself.
const maxWebhookBody = 1 << 20 // 1 MiB

// HandleExample handles POST /examples/webhook:
// it verifies X-Signature (hex of HMAC-SHA256(body, webhook_secret)) and answers 401 on failure.
func (h *WebhookHandler) HandleExample(w http.ResponseWriter, r *http.Request) {
	// Raw routes skip codegen; without a method check, GET/PUT/DELETE would all reach the verification logic.
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, ok := h.readVerified(w, r)
	if !ok {
		return
	}

	// Raw routes run outside the middleware chain; take the client IP the framework computed into r.Context() from
	// this port's client_ip config. Do not read X-Forwarded-For / X-Real-IP yourself: anyone can forge them.
	slog.InfoContext(r.Context(), "webhook received",
		"bytes", len(body), "remote_ip", metadata.GetRemoteIP(r.Context()))
	// TODO: parse the body and call the biz use case

	w.WriteHeader(http.StatusOK)
}

// SignatureFilter returns a pre-routing filter (mounted with sc.Filter) that verifies requests whose path starts
// with prefix using the same rules as HandleExample, and passes all other requests through.
//
// It is used instead of verifying inside a single raw route because, when the signature must cover contract
// endpoints, their handlers decode the body before entering the middleware chain, so the chain never sees the raw
// bytes; the filter runs before routing and decoding. The cost is that none of the chain's protection
// (authentication, logging, tracing, panic recovery, error mapping) has run yet, so this does only the signature
// check and leaves all business to downstream.
//
// The filter applies to every request on this port (including the framework's liveness check); outside the prefix
// it must neither block nor read the body. The prefix match and the router use the same r.URL.Path, so both see the
// same path.
func (h *WebhookHandler) SignatureFilter(prefix string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasPrefix(r.URL.Path, prefix) {
				next.ServeHTTP(w, r)
				return
			}
			body, ok := h.readVerified(w, r)
			if !ok {
				return
			}
			// Verification has read the body to the end; unless it is put back, downstream (contract endpoint decoding,
			// raw route handlers) reads nothing.
			r.Body = io.NopCloser(bytes.NewReader(body))
			next.ServeHTTP(w, r)
		})
	}
}

// readVerified reads the raw request body and verifies X-Signature; on failure it has written the response and
// returns false. It looks only at bytes and signature, not the method: method gates belong to each route.
func (h *WebhookHandler) readVerified(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	// MaxBytesReader rather than LimitReader: the latter returns normally at the limit, so an oversized request is
	// silently truncated and still verified; the signature fails and the answer is 401, pointing the investigation
	// the wrong way. The former errors beyond the limit, so the handler can honestly answer 413.
	r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return nil, false
		}
		http.Error(w, "read body failed", http.StatusBadRequest)
		return nil, false
	}

	// A missing secret fails closed: the template ships no usable default secret, so a missing config refuses
	// service instead of verifying with a value everyone can see.
	secret := h.bootstrap.Get().GetCustom().GetWebhookSecret()
	if secret == "" {
		slog.ErrorContext(r.Context(), "webhook secret not configured")
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return nil, false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(r.Header.Get("X-Signature"))) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return nil, false
	}
	return body, true
}

// SignedEchoHandler handles POST /examples/signed/echo: SignatureFilter has verified the request; this writes the
// client IP back to show how to read the client IP outside the middleware chain.
//
// POST only: the signature covers the request body, and this route receives callbacks with a body. The filter runs
// before routing, so a request with the wrong method is still verified first and only gets 405 here.
func SignedEchoHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Outside the chain, read the client IP only from r.Context(): the framework computes it there before routing
		// from this port's client_ip config. Do not read X-Forwarded-For / X-Real-IP yourself.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, metadata.GetRemoteIP(r.Context())+"\n")
	}
}
