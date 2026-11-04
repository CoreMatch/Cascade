package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"cascade/internal/auth"
	"cascade/internal/backend"
	"cascade/internal/database"
)

// assertErrorEnvelope pins the unified management-API error shape:
// {"success":false,"code":...,"message":...,"meta":{"request_id":...}}.
func assertErrorEnvelope(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantCode string) string {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body %s)", recorder.Code, wantStatus, recorder.Body.String())
	}
	var envelope struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Message string `json:"message"`
		Meta    struct {
			RequestID string `json:"request_id"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("error body is not JSON: %v (%s)", err, recorder.Body.String())
	}
	if envelope.Success {
		t.Fatalf("error envelope reports success=true: %s", recorder.Body.String())
	}
	if envelope.Code != wantCode {
		t.Fatalf("code = %q, want %q", envelope.Code, wantCode)
	}
	if envelope.Message == "" {
		t.Fatalf("error envelope message is empty: %s", recorder.Body.String())
	}
	if len(envelope.Meta.RequestID) != 32 || strings.Trim(envelope.Meta.RequestID, "0123456789abcdef") != "" {
		t.Fatalf("request_id = %q, want 32 lowercase hex characters", envelope.Meta.RequestID)
	}
	return envelope.Meta.RequestID
}

func doRequest(router *gin.Engine, method, target, body string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	router.ServeHTTP(recorder, httptest.NewRequest(method, target, reader))
	return recorder
}

func TestReadyzAndUnknownRoutesUseErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := ProxyRouter(&fakeProxyService{ready: false}, nil, nil)

	first := assertErrorEnvelope(t,
		doRequest(router, http.MethodGet, "/readyz", ""),
		http.StatusServiceUnavailable, codeCascadeBackendUnavailable)
	second := assertErrorEnvelope(t,
		doRequest(router, http.MethodGet, "/readyz", ""),
		http.StatusServiceUnavailable, codeCascadeBackendUnavailable)
	if first == second {
		t.Fatalf("request_id was reused across responses: %s", first)
	}

	assertErrorEnvelope(t,
		doRequest(router, http.MethodGet, "/definitely/not/registered", ""),
		http.StatusNotFound, codeRouteNotFound)

	// Ready path keeps the legacy success shape.
	recorder := doRequest(router, http.MethodGet, "/healthz", "")
	if recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("healthz response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestMalformedJSONBodiesReportInvalidJSONBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := ProxyRouter(&fakeProxyService{ready: true}, nil, nil)

	for _, target := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/rooms"},
		{http.MethodPut, "/api/rooms/AB123/settings"},
		{http.MethodPost, "/api/rooms/AB123/kick"},
		{http.MethodPost, "/api/rooms/AB123/ban"},
	} {
		t.Run(target.method+" "+target.path, func(t *testing.T) {
			assertErrorEnvelope(t,
				doRequest(router, target.method, target.path, "{not-json"),
				http.StatusBadRequest, codeInvalidJSONBody)
		})
	}
}

func TestForwardedRoutingErrorsUseEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, test := range []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "room claimed by multiple backends",
			err:        backend.ErrRoomConflict,
			wantStatus: http.StatusConflict,
			wantCode:   codeCascadeRoomConflict,
		},
		{
			name:       "owning backend unhealthy",
			err:        backend.ErrBackendDown,
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   codeCascadeBackendUnavailable,
		},
		{
			name:       "unclassified forwarding failure",
			err:        errors.New("dial tcp: connection refused"),
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   codeCascadeBackendUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeProxyService{roomErr: test.err}
			router := ProxyRouter(service, nil, nil)
			assertErrorEnvelope(t,
				doRequest(router, http.MethodGet, "/api/rooms/AB123", ""),
				test.wantStatus, test.wantCode)
			assertErrorEnvelope(t,
				doRequest(router, http.MethodPost, "/api/rooms", `{"code":"AB123"}`),
				test.wantStatus, test.wantCode)
		})
	}
}

// TestForbiddenPropagatesAcrossAllRoomEndpoints is the management-side half
// of the trust-strategy matrix: whenever the backend rejects a request with
// the stable 403 FORBIDDEN code (contract "Backend trust strategies"), every
// room facade endpoint must forward that code and message verbatim instead
// of substituting its own error.
func TestForbiddenPropagatesAcrossAllRoomEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	forbidden := &backend.APIError{Status: http.StatusForbidden, Code: "FORBIDDEN", Message: "source IP is not trusted"}

	for _, target := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/rooms/AB123", ""},
		{http.MethodDelete, "/api/rooms/AB123", ""},
		{http.MethodPut, "/api/rooms/AB123/settings", `{"title":"t"}`},
		{http.MethodPost, "/api/rooms/AB123/kick", `{"playerId":"p1"}`},
		{http.MethodPost, "/api/rooms/AB123/ban", `{"playerId":"p1"}`},
		{http.MethodGet, "/api/rooms/AB123/players", ""},
		{http.MethodPost, "/api/rooms", `{"code":"AB123"}`},
	} {
		t.Run(target.method+" "+target.path, func(t *testing.T) {
			service := &fakeProxyService{roomErr: forbidden}
			router := ProxyRouter(service, nil, nil)
			assertErrorEnvelope(t,
				doRequest(router, target.method, target.path, target.body),
				http.StatusForbidden, "FORBIDDEN")
		})
	}
}

// TestBackendContractCodesForwardedVerbatim checks that every stable
// UPPER_SNAKE code a backend may answer with keeps its code, status, and
// message when passed through the facade.
func TestBackendContractCodesForwardedVerbatim(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		status int
		code   string
	}{
		{http.StatusBadRequest, "INVALID_REQUEST"},
		{http.StatusForbidden, "FORBIDDEN"},
		{http.StatusNotFound, "ROOM_NOT_FOUND"},
		{http.StatusConflict, "ROOM_CONFLICT"},
		{http.StatusServiceUnavailable, "BACKEND_NOT_READY"},
		{http.StatusInternalServerError, "INTERNAL_ERROR"},
	} {
		t.Run(fmt.Sprintf("%d %s", test.status, test.code), func(t *testing.T) {
			service := &fakeProxyService{roomErr: &backend.APIError{
				Status: test.status, Code: test.code, Message: "backend message",
			}}
			router := ProxyRouter(service, nil, nil)
			recorder := doRequest(router, http.MethodGet, "/api/rooms/AB123", "")
			assertErrorEnvelope(t, recorder, test.status, test.code)
			if !strings.Contains(recorder.Body.String(), "backend message") {
				t.Fatalf("backend message was dropped: %s", recorder.Body.String())
			}
		})
	}
}

// authCoreStub is a minimal HRPAuth Core: OAuth2 token endpoint, credential
// verification, and internal-key issuance.
type authCoreStub struct {
	server       *httptest.Server
	verifyStatus int
	verifyBody   string
}

func newAuthCoreStub(t *testing.T) *authCoreStub {
	t.Helper()
	stub := &authCoreStub{verifyStatus: http.StatusOK, verifyBody: `{"uuid":"ha-1","email":"a@b.c","username":"player"}`}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/token":
			_, _ = fmt.Fprint(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`)
		case "/internal/verify-credentials":
			w.WriteHeader(stub.verifyStatus)
			_, _ = fmt.Fprint(w, stub.verifyBody)
		case "/services/auth-key":
			_, _ = fmt.Fprint(w, `{"internal_key":"test-internal-key"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func newTestAuthClient(t *testing.T, stub *authCoreStub) *auth.Client {
	t.Helper()
	client := auth.New(stub.server.URL, "client-id", "client-secret", "cascade")
	if err := client.FetchInternalKey(); err != nil {
		t.Fatalf("fetch internal key: %v", err)
	}
	return client
}

func TestAuthVerifyEnvelopeAcrossOutcomes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := newAuthCoreStub(t)
	router := ProxyRouter(&fakeProxyService{ready: true}, newTestAuthClient(t, stub), nil)

	// Happy path keeps the legacy success shape.
	recorder := doRequest(router, http.MethodPost, "/api/auth/verify", `{"identifier":"a@b.c","password":"pw"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("verify status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	var user map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &user); err != nil || user["uuid"] != "ha-1" {
		t.Fatalf("verify response = %s (err %v)", recorder.Body.String(), err)
	}

	// Invalid credentials -> 401 invalid_credentials.
	stub.verifyStatus, stub.verifyBody = http.StatusUnauthorized, `{}`
	assertErrorEnvelope(t,
		doRequest(router, http.MethodPost, "/api/auth/verify", `{"identifier":"a@b.c","password":"wrong"}`),
		http.StatusUnauthorized, codeInvalidCredentials)

	// Upstream failure -> 502 internal_error (fail closed, never pass through).
	stub.verifyStatus, stub.verifyBody = http.StatusInternalServerError, "boom"
	assertErrorEnvelope(t,
		doRequest(router, http.MethodPost, "/api/auth/verify", `{"identifier":"a@b.c","password":"pw"}`),
		http.StatusBadGateway, codeInternalError)

	// Malformed body -> 400 invalid_json_body.
	assertErrorEnvelope(t,
		doRequest(router, http.MethodPost, "/api/auth/verify", "{not-json"),
		http.StatusBadRequest, codeInvalidJSONBody)
}

// fakeStore is an in-memory database.Store for API-layer tests.
type fakeStore struct {
	binding    *database.PlayerBinding
	bindings   []database.PlayerBinding
	getErr     error
	listErr    error
	claimErr   error
	createErr  error
	lastClaim  [2]string
	lastCreate [2]string
}

func (f *fakeStore) GetBindingByGameUUID(string) (*database.PlayerBinding, error) {
	return f.binding, f.getErr
}

func (f *fakeStore) GetBindingsByHAUUID(string) ([]database.PlayerBinding, error) {
	if f.bindings == nil {
		return []database.PlayerBinding{}, f.listErr
	}
	return f.bindings, f.listErr
}

func (f *fakeStore) CreateProxyBinding(gameUUID, playerName string) error {
	f.lastCreate = [2]string{gameUUID, playerName}
	return f.createErr
}

func (f *fakeStore) ClaimBinding(gameUUID, haUserUUID string) error {
	f.lastClaim = [2]string{gameUUID, haUserUUID}
	return f.claimErr
}

func (f *fakeStore) GetLang(string) (string, error)            { return "en", nil }
func (f *fakeStore) SetLang(string, string) error              { return nil }
func (f *fakeStore) CleanupStaleProxyBindings() (int64, error) { return 0, nil }
func (f *fakeStore) Close() error                              { return nil }

func TestClaimBindingEnvelopeAcrossOutcomes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := newAuthCoreStub(t)

	t.Run("happy path", func(t *testing.T) {
		store := &fakeStore{}
		router := ProxyRouter(&fakeProxyService{}, newTestAuthClient(t, stub), store)
		recorder := doRequest(router, http.MethodPost, "/api/auth/claim", `{"gameUUID":"g1","haUserUUID":"ha-1"}`)
		if recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != `{"success":true}` {
			t.Fatalf("claim response = %d %s", recorder.Code, recorder.Body.String())
		}
		if store.lastClaim != [2]string{"g1", "ha-1"} {
			t.Fatalf("claim was not forwarded: %+v", store.lastClaim)
		}
	})

	t.Run("unknown or already claimed binding is 404", func(t *testing.T) {
		store := &fakeStore{claimErr: fmt.Errorf("game_uuid g1: %w", database.ErrBindingNotFound)}
		router := ProxyRouter(&fakeProxyService{}, newTestAuthClient(t, stub), store)
		assertErrorEnvelope(t,
			doRequest(router, http.MethodPost, "/api/auth/claim", `{"gameUUID":"g1","haUserUUID":"ha-1"}`),
			http.StatusNotFound, codeCascadeBindingNotFound)
	})

	t.Run("database failure is 500 internal_error", func(t *testing.T) {
		store := &fakeStore{claimErr: errors.New("disk full")}
		router := ProxyRouter(&fakeProxyService{}, newTestAuthClient(t, stub), store)
		assertErrorEnvelope(t,
			doRequest(router, http.MethodPost, "/api/auth/claim", `{"gameUUID":"g1","haUserUUID":"ha-1"}`),
			http.StatusInternalServerError, codeInternalError)
	})

	t.Run("database not configured is 500", func(t *testing.T) {
		router := ProxyRouter(&fakeProxyService{}, newTestAuthClient(t, stub), nil)
		assertErrorEnvelope(t,
			doRequest(router, http.MethodPost, "/api/auth/claim", `{"gameUUID":"g1","haUserUUID":"ha-1"}`),
			http.StatusInternalServerError, codeCascadeDatabaseNotConfigured)
	})

	t.Run("malformed body is 400", func(t *testing.T) {
		router := ProxyRouter(&fakeProxyService{}, newTestAuthClient(t, stub), &fakeStore{})
		assertErrorEnvelope(t,
			doRequest(router, http.MethodPost, "/api/auth/claim", "{not-json"),
			http.StatusBadRequest, codeInvalidJSONBody)
	})
}

func TestBindingStatusAndListEnvelopeAcrossOutcomes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := newAuthCoreStub(t)
	authClient := newTestAuthClient(t, stub)

	t.Run("status happy path keeps legacy shape", func(t *testing.T) {
		store := &fakeStore{binding: &database.PlayerBinding{
			GameUUID: "g1", HAUserUUID: "ha-1", PlayerName: "player", CBH: 1,
			QuickAuth: true, LastLogin: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC),
		}}
		router := ProxyRouter(&fakeProxyService{}, authClient, store)
		recorder := doRequest(router, http.MethodGet, "/api/auth/status/g1", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status response = %d %s", recorder.Code, recorder.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["gameUUID"] != "g1" || payload["haUserUUID"] != "ha-1" || payload["cbh"] != float64(1) {
			t.Fatalf("status payload lost fields: %s", recorder.Body.String())
		}
	})

	t.Run("status miss is 404 cascade_binding_not_found", func(t *testing.T) {
		router := ProxyRouter(&fakeProxyService{}, authClient, &fakeStore{})
		assertErrorEnvelope(t,
			doRequest(router, http.MethodGet, "/api/auth/status/unknown", ""),
			http.StatusNotFound, codeCascadeBindingNotFound)
	})

	t.Run("status lookup failure is 500 internal_error", func(t *testing.T) {
		router := ProxyRouter(&fakeProxyService{}, authClient, &fakeStore{getErr: errors.New("connection lost")})
		assertErrorEnvelope(t,
			doRequest(router, http.MethodGet, "/api/auth/status/g1", ""),
			http.StatusInternalServerError, codeInternalError)
	})

	t.Run("bindings list happy path", func(t *testing.T) {
		store := &fakeStore{bindings: []database.PlayerBinding{{
			GameUUID: "g1", HAUserUUID: "ha-1", PlayerName: "player",
			LastLogin: time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC),
		}}}
		router := ProxyRouter(&fakeProxyService{}, authClient, store)
		recorder := doRequest(router, http.MethodGet, "/api/auth/bindings/ha-1", "")
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"gameUUID":"g1"`) {
			t.Fatalf("bindings response = %d %s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("bindings list failure is 500 internal_error", func(t *testing.T) {
		router := ProxyRouter(&fakeProxyService{}, authClient, &fakeStore{listErr: errors.New("connection lost")})
		assertErrorEnvelope(t,
			doRequest(router, http.MethodGet, "/api/auth/bindings/ha-1", ""),
			http.StatusInternalServerError, codeInternalError)
	})

	t.Run("database not configured is 500 on both endpoints", func(t *testing.T) {
		router := ProxyRouter(&fakeProxyService{}, authClient, nil)
		assertErrorEnvelope(t,
			doRequest(router, http.MethodGet, "/api/auth/status/g1", ""),
			http.StatusInternalServerError, codeCascadeDatabaseNotConfigured)
		assertErrorEnvelope(t,
			doRequest(router, http.MethodGet, "/api/auth/bindings/ha-1", ""),
			http.StatusInternalServerError, codeCascadeDatabaseNotConfigured)
	})
}

func TestInternalKeyGateUsesEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := newAuthCoreStub(t)

	t.Run("missing auth client rejects every internal route", func(t *testing.T) {
		router := ProxyRouter(&fakeProxyService{}, nil, nil)
		assertErrorEnvelope(t,
			doRequest(router, http.MethodGet, "/internal/heartbeat", ""),
			http.StatusUnauthorized, codeCascadeInvalidInternalKey)
		assertErrorEnvelope(t,
			doRequest(router, http.MethodPost, "/internal/proxy-register", `{"gameUUID":"g1","playerName":"p"}`),
			http.StatusUnauthorized, codeCascadeInvalidInternalKey)
	})

	t.Run("wrong key is rejected", func(t *testing.T) {
		router := ProxyRouter(&fakeProxyService{}, newTestAuthClient(t, stub), nil)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/internal/heartbeat", nil)
		request.Header.Set("X-Internal-Key", "wrong-key")
		router.ServeHTTP(recorder, request)
		assertErrorEnvelope(t, recorder, http.StatusUnauthorized, codeCascadeInvalidInternalKey)
	})

	t.Run("issued key passes heartbeat", func(t *testing.T) {
		router := ProxyRouter(&fakeProxyService{}, newTestAuthClient(t, stub), nil)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/internal/heartbeat", nil)
		request.Header.Set("X-Internal-Key", "test-internal-key")
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != `{"status":"ok"}` {
			t.Fatalf("heartbeat response = %d %s", recorder.Code, recorder.Body.String())
		}
	})
}

func TestProxyRegisterEnvelopeAcrossOutcomes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := newAuthCoreStub(t)
	authClient := newTestAuthClient(t, stub)

	withKey := func(router *gin.Engine, method, target, body string) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(method, target, strings.NewReader(body))
		request.Header.Set("X-Internal-Key", "test-internal-key")
		router.ServeHTTP(recorder, request)
		return recorder
	}

	t.Run("happy path registers the binding", func(t *testing.T) {
		store := &fakeStore{}
		router := ProxyRouter(&fakeProxyService{}, authClient, store)
		recorder := withKey(router, http.MethodPost, "/internal/proxy-register", `{"gameUUID":"g1","playerName":"p1"}`)
		if recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != `{"success":true}` {
			t.Fatalf("proxy-register response = %d %s", recorder.Code, recorder.Body.String())
		}
		if store.lastCreate != [2]string{"g1", "p1"} {
			t.Fatalf("proxy-register was not forwarded: %+v", store.lastCreate)
		}
	})

	t.Run("malformed body is 400", func(t *testing.T) {
		router := ProxyRouter(&fakeProxyService{}, authClient, &fakeStore{})
		assertErrorEnvelope(t,
			withKey(router, http.MethodPost, "/internal/proxy-register", "{not-json"),
			http.StatusBadRequest, codeInvalidJSONBody)
	})

	t.Run("missing required field is 400", func(t *testing.T) {
		router := ProxyRouter(&fakeProxyService{}, authClient, &fakeStore{})
		assertErrorEnvelope(t,
			withKey(router, http.MethodPost, "/internal/proxy-register", `{"gameUUID":"g1"}`),
			http.StatusBadRequest, codeInvalidJSONBody)
	})

	t.Run("store failure is 500 internal_error", func(t *testing.T) {
		router := ProxyRouter(&fakeProxyService{}, authClient, &fakeStore{createErr: errors.New("disk full")})
		assertErrorEnvelope(t,
			withKey(router, http.MethodPost, "/internal/proxy-register", `{"gameUUID":"g1","playerName":"p1"}`),
			http.StatusInternalServerError, codeInternalError)
	})

	t.Run("database not configured is 500", func(t *testing.T) {
		router := ProxyRouter(&fakeProxyService{}, authClient, nil)
		assertErrorEnvelope(t,
			withKey(router, http.MethodPost, "/internal/proxy-register", `{"gameUUID":"g1","playerName":"p1"}`),
			http.StatusInternalServerError, codeCascadeDatabaseNotConfigured)
	})
}
