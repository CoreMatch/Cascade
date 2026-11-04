package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// tokenOK serves a successful OAuth2 client_credentials response.
func tokenOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`)
}

// newTestClient points a Client at the given server with a short timeout.
func newTestClient(serverURL string) *Client {
	c := New(serverURL, "client-id", "client-secret", "Cascade")
	c.http.Timeout = 2 * time.Second
	return c
}

func TestTokenFailureBlocksAllDownstreamCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"error":"invalid_client"}`)
			return
		}
		t.Errorf("unexpected call to %s after token failure", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := newTestClient(server.URL)

	if _, err := client.Register("a@b.c", "player", "secret123"); err == nil ||
		!strings.Contains(err.Error(), "get token") {
		t.Fatalf("Register error = %v, want token failure", err)
	}
	if _, err := client.VerifyCredentials("user", "pw"); err == nil ||
		!strings.Contains(err.Error(), "get token") {
		t.Fatalf("VerifyCredentials error = %v, want token failure", err)
	}
	if err := client.RegisterPresence(120); err == nil ||
		!strings.Contains(err.Error(), "get token") {
		t.Fatalf("RegisterPresence error = %v, want token failure", err)
	}
	if err := client.FetchInternalKey(); err == nil ||
		!strings.Contains(err.Error(), "get token") {
		t.Fatalf("FetchInternalKey error = %v, want token failure", err)
	}
}

func TestVerifyCredentialsDistinguishesInvalidFromServiceErrors(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		body      string
		wantUser  bool
		wantErr   string
	}{
		{
			name:     "401 means invalid credentials, not an error",
			status:   http.StatusUnauthorized,
			body:     `{"message":"bad credentials"}`,
			wantUser: false,
		},
		{
			name:     "200 returns the verified user",
			status:   http.StatusOK,
			body:     `{"uuid":"ha-1","email":"a@b.c","username":"alice"}`,
			wantUser: true,
		},
		{
			name:    "500 is a service error",
			status:  http.StatusInternalServerError,
			body:    "core exploded",
			wantErr: "unexpected status 500",
		},
		{
			name:    "malformed 200 body fails closed",
			status:  http.StatusOK,
			body:    `{"uuid":`,
			wantErr: "decode response",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/oauth/token":
					tokenOK(w)
				case "/internal/verify-credentials":
					w.WriteHeader(test.status)
					_, _ = fmt.Fprint(w, test.body)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := newTestClient(server.URL)

			user, err := client.VerifyCredentials("alice", "pw")
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("VerifyCredentials error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyCredentials: %v", err)
			}
			if test.wantUser && (user == nil || user.UUID != "ha-1") {
				t.Fatalf("user = %+v, want the verified ha-1 user", user)
			}
			if !test.wantUser && user != nil {
				t.Fatalf("user = %+v, want nil for invalid credentials", user)
			}
		})
	}
}

func TestRegisterSurfacesCoreRejections(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{
			name:   "200 succeeds",
			status: http.StatusOK,
			body:   `{"success":true,"uid":7,"uuid":"ha-7","message":"ok"}`,
		},
		{
			name:    "409 carries the core message",
			status:  http.StatusConflict,
			body:    `{"success":false,"message":"email already registered"}`,
			wantErr: "email already registered",
		},
		{
			name:    "malformed body fails closed",
			status:  http.StatusOK,
			body:    `{`,
			wantErr: "decode response",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/oauth/token":
					tokenOK(w)
				case "/register":
					w.WriteHeader(test.status)
					_, _ = fmt.Fprint(w, test.body)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := newTestClient(server.URL)

			result, err := client.Register("a@b.c", "player", "secret123")
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("Register error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Register: %v", err)
			}
			if result == nil || result.UUID != "ha-7" {
				t.Fatalf("result = %+v, want the ha-7 registration", result)
			}
		})
	}
}

func TestFetchInternalKeyAndValidationFailClosed(t *testing.T) {
	// No key fetched yet: every validation must fail closed.
	fresh := New("http://unused.invalid", "id", "secret", "Cascade")
	if fresh.ValidateInternalKey("anything") {
		t.Fatal("validation succeeded with no key fetched")
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			tokenOK(w)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, "core busy")
	}))
	defer failing.Close()
	client := newTestClient(failing.URL)
	if err := client.FetchInternalKey(); err == nil ||
		!strings.Contains(err.Error(), "auth-key request failed") {
		t.Fatalf("FetchInternalKey error = %v, want non-2xx failure", err)
	}
	if client.ValidateInternalKey("anything") {
		t.Fatal("validation succeeded after a failed key fetch")
	}

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			tokenOK(w)
			return
		}
		_, _ = fmt.Fprint(w, `{"internal_key":"k-123"}`)
	}))
	defer ok.Close()
	client = newTestClient(ok.URL)
	if err := client.FetchInternalKey(); err != nil {
		t.Fatalf("FetchInternalKey: %v", err)
	}
	if !client.ValidateInternalKey("k-123") {
		t.Fatal("correct internal key rejected")
	}
	if client.ValidateInternalKey("k-999") || client.ValidateInternalKey("") {
		t.Fatal("wrong or empty internal key accepted")
	}
}

func TestRequestsAbortOnTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			tokenOK(w)
			return
		}
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := newTestClient(server.URL)
	client.http.Timeout = 50 * time.Millisecond

	start := time.Now()
	if err := client.RegisterPresence(120); err == nil {
		t.Fatal("expected the presence post to time out")
	}
	if time.Since(start) > 400*time.Millisecond {
		t.Fatalf("timeout was not enforced promptly: %v", time.Since(start))
	}
}
