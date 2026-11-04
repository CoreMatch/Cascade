package backend

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cascade/internal/config"
)

// plainTestClient builds a Client resolved to plaintext transport against
// the given control server (no certificate material involved).
func plainTestClient(t *testing.T, serverURL, backendID string) *Client {
	t.Helper()
	client, err := NewClient(config.BackendConfig{
		ID: backendID, ControlURL: serverURL, GameAddress: "127.0.0.1:1",
		TrustMode: TrustOpen,
	}, "", "", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func TestHealthRejectsNotReadyMismatchAndAPIErrors(t *testing.T) {
	for _, test := range []struct {
		name      string
		payload   string
		status    int
		wantErr   string
		wantCode  string
	}{
		{
			name:     "backend reports not ready",
			payload:  `{"backendId":"backend-a","status":"not_ready"}`,
			status:   http.StatusOK,
			wantErr:  "not ready",
		},
		{
			name:     "backend ID mismatch",
			payload:  `{"backendId":"some-other-backend","status":"ready"}`,
			status:   http.StatusOK,
			wantErr:  "mismatched backend ID",
		},
		{
			name:     "trust strategy rejection surfaces the stable code",
			payload:  `{"code":"FORBIDDEN","message":"source IP is not trusted"}`,
			status:   http.StatusForbidden,
			wantErr:  "source IP is not trusted",
			wantCode: "FORBIDDEN",
		},
		{
			name:     "backend error body is preserved",
			payload:  `{"code":"BACKEND_NOT_READY","message":"game listener down"}`,
			status:   http.StatusServiceUnavailable,
			wantErr:  "game listener down",
			wantCode: "BACKEND_NOT_READY",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, test.payload)
			}))
			defer server.Close()
			client := plainTestClient(t, server.URL, "backend-a")

			err := client.Health(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Health error = %v, want substring %q", err, test.wantErr)
			}
			if test.wantCode != "" {
				apiErr, ok := err.(*APIError)
				if !ok || apiErr.Code != test.wantCode || apiErr.Status != test.status {
					t.Fatalf("APIError = %+v (ok=%v), want code %s status %d", apiErr, ok, test.wantCode, test.status)
				}
			}
		})
	}
}

func TestRoomsRejectsMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"backendId":"backend-a","generatedAt":"2026-11-05T00:00:00Z","rooms":[`)
	}))
	defer server.Close()
	client := plainTestClient(t, server.URL, "backend-a")

	if _, err := client.Rooms(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "decode backend response") {
		t.Fatalf("Rooms error = %v, want decode failure", err)
	}
}

func TestNonJSONErrorBodyBecomesBackendErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, "upstream exploded")
	}))
	defer server.Close()
	client := plainTestClient(t, server.URL, "backend-a")

	err := client.Health(context.Background())
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("Health error type = %T (%v), want *APIError", err, err)
	}
	if apiErr.Code != "BACKEND_ERROR" || apiErr.Message != "upstream exploded" || apiErr.Status != http.StatusBadGateway {
		t.Fatalf("APIError = %+v, want BACKEND_ERROR with the raw body preserved", apiErr)
	}
}

// dropConnection aborts the request at the transport level (the only kind
// of failure the client retries).
func dropConnection(w http.ResponseWriter) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		panic("test server does not support hijacking")
	}
	conn, _, err := hijacker.Hijack()
	if err != nil {
		panic(err)
	}
	_ = conn.Close()
}

func TestIdempotentRequestsRetryAndNonIdempotentDoNot(t *testing.T) {
	var healthCalls, postCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			if healthCalls.Add(1) < 3 {
				dropConnection(w)
				return
			}
			_, _ = fmt.Fprint(w, `{"backendId":"backend-a","status":"ready"}`)
		case "/v1/rooms/create":
			postCalls.Add(1)
			dropConnection(w)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := plainTestClient(t, server.URL, "backend-a")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Health(ctx); err != nil {
		t.Fatalf("Health should succeed once the transport recovers: %v", err)
	}
	if got := healthCalls.Load(); got != 3 {
		t.Fatalf("idempotent GET attempts = %d, want 3 (initial + 2 retries)", got)
	}

	if _, _, err := client.Request(ctx, http.MethodPost, "/rooms/create", map[string]string{"code": "GW01"}); err == nil {
		t.Fatal("POST should surface the transport failure")
	}
	if got := postCalls.Load(); got != 1 {
		t.Fatalf("non-idempotent POST attempts = %d, want exactly 1", got)
	}
}

// TestHTTPErrorStatusesAreNotRetried pins the contract-side retry rule:
// only transport failures are retried; a backend answering with an error
// status is surfaced to the caller immediately.
func TestHTTPErrorStatusesAreNotRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprint(w, `{"code":"BACKEND_NOT_READY","message":"busy"}`)
	}))
	defer server.Close()
	client := plainTestClient(t, server.URL, "backend-a")

	if err := client.Health(context.Background()); err == nil {
		t.Fatal("expected the unhealthy backend to surface an error")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("HTTP error status attempts = %d, want exactly 1 (no retry)", got)
	}
}

func TestRequestHonorsContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = fmt.Fprint(w, `{"backendId":"backend-a","status":"ready"}`)
	}))
	defer server.Close()
	client := plainTestClient(t, server.URL, "backend-a")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := client.Health(ctx)
	if err == nil {
		t.Fatal("expected the bounded context to abort the slow backend")
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("context deadline was not honored promptly: %v", time.Since(start))
	}
}
