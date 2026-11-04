package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBindIdentityHappyPath pins the successful bind exchange: PUT
// /v1/sessions/{sessionID}/identity with the JWT body, answered by 204.
func TestBindIdentityHappyPath(t *testing.T) {
	var gotPath, gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal(body, &payload)
		gotToken = payload.Token
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := plainTestClient(t, server.URL, "backend-a")

	if err := client.BindIdentity(context.Background(), "session-1", "jwt-token"); err != nil {
		t.Fatalf("BindIdentity: %v", err)
	}
	if gotPath != "/v1/sessions/session-1/identity" {
		t.Fatalf("bind path = %q", gotPath)
	}
	if gotToken != "jwt-token" {
		t.Fatalf("bind token = %q", gotToken)
	}
}

// TestBindIdentitySurfacesContractErrorCodes covers the stable backend error
// codes the identity endpoint may answer with (contract "Session state
// machine" and the OpenAPI responses): each must surface as an *APIError
// carrying the backend's status and code verbatim, never as a retry.
func TestBindIdentitySurfacesContractErrorCodes(t *testing.T) {
	for _, test := range []struct {
		status int
		code   string
	}{
		{http.StatusBadRequest, "SESSION_IDENTITY_INVALID"},
		{http.StatusForbidden, "FORBIDDEN"},
		{http.StatusNotFound, "SESSION_NOT_FOUND"},
		{http.StatusConflict, "SESSION_IDENTITY_REPLAYED"},
		{http.StatusGone, "SESSION_EXPIRED"},
		{http.StatusServiceUnavailable, "BACKEND_NOT_READY"},
	} {
		t.Run(fmt.Sprintf("%d %s", test.status, test.code), func(t *testing.T) {
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(test.status)
				_, _ = fmt.Fprintf(w, `{"code":%q,"message":"backend says no"}`, test.code)
			}))
			defer server.Close()
			client := plainTestClient(t, server.URL, "backend-a")

			err := client.BindIdentity(context.Background(), "session-1", "jwt-token")
			apiErr, ok := err.(*APIError)
			if !ok {
				t.Fatalf("BindIdentity error type = %T (%v), want *APIError", err, err)
			}
			if apiErr.Status != test.status || apiErr.Code != test.code || apiErr.Message != "backend says no" {
				t.Fatalf("APIError = %+v, want status %d code %s", apiErr, test.status, test.code)
			}
			if calls != 1 {
				t.Fatalf("backend error status was retried: %d attempts", calls)
			}
		})
	}
}

// TestRequestForwardsRawBodyAndStatus covers the generic room-facade
// forwarding primitive on the success side.
func TestRequestForwardsRawBodyAndStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rooms/AB123/players" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"players":[]}`)
	}))
	defer server.Close()
	client := plainTestClient(t, server.URL, "backend-a")

	raw, status, err := client.Request(context.Background(), http.MethodGet, "/rooms/AB123/players", nil)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if status != http.StatusOK || string(raw) != `{"players":[]}` {
		t.Fatalf("Request = (%d, %s), want raw body forwarded unchanged", status, raw)
	}
}
