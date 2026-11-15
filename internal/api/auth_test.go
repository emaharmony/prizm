package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newAuthTestServer returns a Server wired only with the fields the auth/CORS
// middleware needs, plus a handler that records whether it was reached.
func newAuthTestServer(token string, origins []string) (*Server, *bool) {
	reached := false
	s := &Server{authToken: token, allowedOrigins: origins}
	return s, &reached
}

func TestAuthMiddleware_NoTokenAllowsEverything(t *testing.T) {
	s, reached := newAuthTestServer("", nil)
	h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/abc/grant", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !*reached || rec.Code != http.StatusOK {
		t.Fatalf("no-token mode should allow POST: reached=%v code=%d", *reached, rec.Code)
	}
}

func TestAuthMiddleware_BlocksUnauthenticatedMutation(t *testing.T) {
	s, reached := newAuthTestServer("secret-token", nil)
	h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
	}))

	// No Authorization header on a state-changing request.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/approvals/abc/grant", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if *reached {
		t.Fatal("handler must not be reached without a valid token")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
}

func TestAuthMiddleware_AllowsValidBearer(t *testing.T) {
	s, reached := newAuthTestServer("secret-token", nil)
	h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/editor/save", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !*reached || rec.Code != http.StatusOK {
		t.Fatalf("valid bearer should pass: reached=%v code=%d", *reached, rec.Code)
	}
}

func TestAuthMiddleware_GetReadIsOpen(t *testing.T) {
	s, reached := newAuthTestServer("secret-token", nil)
	h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	}))

	// GET status is a read; allowed even without a token.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !*reached {
		t.Fatal("read-only GET should be allowed")
	}
}

func TestAuthMiddleware_FreewillRunCollectionRequiresAuth(t *testing.T) {
	s, reached := newAuthTestServer("secret-token", nil)
	h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		w.WriteHeader(http.StatusOK)
	}))
	for _, path := range []string{"/api/v1/freewill/runs", "/api/v1/freewill/runs/"} {
		*reached = false
		unauthenticated := httptest.NewRecorder()
		h.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, path, nil))
		if *reached || unauthenticated.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated collection GET %s reached=%v status=%d, want protected", path, *reached, unauthenticated.Code)
		}
		authorized := httptest.NewRequest(http.MethodGet, path, nil)
		authorized.Header.Set("Authorization", "Bearer secret-token")
		response := httptest.NewRecorder()
		h.ServeHTTP(response, authorized)
		if !*reached || response.Code != http.StatusOK {
			t.Fatalf("authenticated collection GET %s reached=%v status=%d, want allowed", path, *reached, response.Code)
		}
	}
}

func TestAuthMiddleware_SSERequiresAuth(t *testing.T) {
	s, reached := newAuthTestServer("secret-token", nil)
	h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
	}))

	// SSE stream is a GET but must be authenticated.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if *reached || rec.Code != http.StatusUnauthorized {
		t.Fatalf("SSE without token must be 401: reached=%v code=%d", *reached, rec.Code)
	}

	// With a token via query param (EventSource can't set headers) it passes.
	reached2 := false
	h2 := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached2 = true
		w.WriteHeader(http.StatusOK)
	}))
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/events/stream?token=secret-token", nil)
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req2)
	if !reached2 {
		t.Fatal("SSE with valid ?token= should pass")
	}
}

func TestCORS_OnlyAllowlistedOrigins(t *testing.T) {
	s, _ := newAuthTestServer("", []string{"https://editor.local"})
	h := s.corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	// Disallowed origin: no ACAO header reflected.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("disallowed origin reflected: %q", got)
	}

	// Allowlisted origin: reflected exactly (never "*").
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	req2.Header.Set("Origin", "https://editor.local")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if got := rec2.Header().Get("Access-Control-Allow-Origin"); got != "https://editor.local" {
		t.Fatalf("allowlisted origin = %q, want exact echo", got)
	}
}
