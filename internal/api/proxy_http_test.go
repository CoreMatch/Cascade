package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"cascade/internal/backend"
	"cascade/internal/plugin"
)

type fakeProxyService struct {
	ready      bool
	rooms      []backend.Room
	statuses   []backend.Status
	plugins    []plugin.PluginStatus
	createCode string
	createBody any
	method     string
	roomCode   string
	roomSuffix string
	roomBody   any
	roomStatus int
	roomRaw    []byte
	roomErr    error
}

func (f *fakeProxyService) Ready() bool                           { return f.ready }
func (f *fakeProxyService) Rooms() []backend.Room                 { return f.rooms }
func (f *fakeProxyService) BackendStatuses() []backend.Status     { return f.statuses }
func (f *fakeProxyService) PluginStatuses() []plugin.PluginStatus { return f.plugins }

func (f *fakeProxyService) CreateRoom(_ context.Context, code string, body any) ([]byte, int, error) {
	f.createCode, f.createBody = code, body
	if f.roomErr != nil {
		return nil, 0, f.roomErr
	}
	return []byte(`{"code":"` + code + `"}`), http.StatusCreated, nil
}

func (f *fakeProxyService) RoomRequest(_ context.Context, method, code, suffix string, body any) ([]byte, int, error) {
	f.method, f.roomCode, f.roomSuffix, f.roomBody = method, code, suffix, body
	if f.roomErr != nil {
		return nil, 0, f.roomErr
	}
	return f.roomRaw, f.roomStatus, nil
}

func TestProxyRouterPreservesRoomListShapeAndEmptyArray(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &fakeProxyService{rooms: []backend.Room{{
		Code: "AB123", Title: "Lobby", MapName: "map.tmx", MaxPlayers: 8,
		Players: 2, InGame: true, OwnerID: "owner-id", OwnerName: "Owner",
		AdminCount: 1, CreatedAt: time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC),
	}}}
	router := ProxyRouter(service, nil, nil)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/rooms", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET /api/rooms status = %d", response.Code)
	}
	var payload struct {
		Rooms []map[string]any `json:"rooms"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Rooms) != 1 {
		t.Fatalf("room count = %d, want 1", len(payload.Rooms))
	}
	for _, field := range []string{"code", "title", "mapName", "maxPlayers", "players", "inGame", "ownerId", "ownerName", "adminCount", "createdAt"} {
		if _, exists := payload.Rooms[0][field]; !exists {
			t.Errorf("room list omitted legacy field %q", field)
		}
	}

	service.rooms = nil
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/rooms", nil))
	if strings.TrimSpace(response.Body.String()) != `{"rooms":[]}` {
		t.Fatalf("empty room list response = %s, want {\"rooms\":[]}", response.Body.String())
	}
}

func TestProxyRouterForwardsRoomOperationsAndKeepsLegacyDeleteResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &fakeProxyService{roomStatus: http.StatusNoContent}
	router := ProxyRouter(service, nil, nil)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/api/rooms/AB123", nil))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"message":"room deleted"}` {
		t.Fatalf("delete response = %d %s", response.Code, response.Body.String())
	}
	if service.method != http.MethodDelete || service.roomCode != "AB123" || service.roomSuffix != "" {
		t.Fatalf("delete was not routed to its room owner: %+v", service)
	}

	service.roomStatus = http.StatusOK
	service.roomRaw = []byte(`{"code":"AB123","members":[]}`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/rooms/AB123", nil))
	if response.Code != http.StatusOK || response.Body.String() != string(service.roomRaw) {
		t.Fatalf("room detail response = %d %s", response.Code, response.Body.String())
	}
	if service.method != http.MethodGet || service.roomCode != "AB123" {
		t.Fatalf("room detail request was not forwarded: %+v", service)
	}
}

// TestProxyRouterProbes covers the /healthz and /readyz probes
// (contract cascade-proxy.yaml).
func TestProxyRouterProbes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &fakeProxyService{ready: false}
	router := ProxyRouter(service, nil, nil)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"status":"ok"}` {
		t.Fatalf("healthz response = %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assertErrorEnvelope(t, response, http.StatusServiceUnavailable, codeCascadeBackendUnavailable)

	service.ready = true
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"status":"ready"}` {
		t.Fatalf("readyz (ready) response = %d %s", response.Code, response.Body.String())
	}
}

// TestProxyRouterBackends covers GET /api/backends.
func TestProxyRouterBackends(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &fakeProxyService{statuses: []backend.Status{{ID: "backend-a", ControlURL: "http://127.0.0.1:8080"}}}
	router := ProxyRouter(service, nil, nil)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/backends", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if !strings.Contains(response.Body.String(), `"backends"`) {
		t.Fatalf("missing backends key: %s", response.Body.String())
	}
}

// TestProxyRouterPlugins covers GET /api/plugins (contract cascade-proxy.yaml
// /api/plugins): status snapshot with the contract-locked camelCase keys.
func TestProxyRouterPlugins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &fakeProxyService{plugins: []plugin.PluginStatus{{
		Name:            "route-watcher",
		BaseURL:         "http://127.0.0.1:9801",
		Enabled:         true,
		Ready:           true,
		ManifestID:      "route-watcher",
		ManifestVersion: "0.1.0",
		Events:          []string{"player.registered", "player.route", "player.routed"},
		Queued:          0,
		Delivered:       3,
		Dropped:         1,
		Denied:          1,
		FailOpen:        2,
	}}}
	router := ProxyRouter(service, nil, nil)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/plugins", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var body struct {
		Plugins []map[string]any `json:"plugins"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v\n%s", err, response.Body.String())
	}
	if len(body.Plugins) != 1 {
		t.Fatalf("plugins = %d entries, want 1", len(body.Plugins))
	}
	// 契约锁定的键集合必须逐键在场（字段级防线）
	for _, key := range []string{
		"name", "baseUrl", "enabled", "ready", "manifestId", "manifestVersion",
		"events", "queued", "delivered", "dropped", "denied", "failOpen",
	} {
		if _, ok := body.Plugins[0][key]; !ok {
			t.Errorf("contract key %q missing: %v", key, body.Plugins[0])
		}
	}
	if body.Plugins[0]["name"] != "route-watcher" || body.Plugins[0]["baseUrl"] != "http://127.0.0.1:9801" {
		t.Errorf("identity fields wrong: %v", body.Plugins[0])
	}
}

// TestProxyRouterPluginsEmpty covers the no-plugin shape: {"plugins": []},
// never null (contract: empty array when no plugin is configured).
func TestProxyRouterPluginsEmpty(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := ProxyRouter(&fakeProxyService{}, nil, nil)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/plugins", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if got := strings.TrimSpace(response.Body.String()); !strings.Contains(got, `"plugins":[]`) {
		t.Fatalf("empty plugin list must serialize as []: %s", got)
	}
}

// TestInternalRoutesRequireInternalKey covers the fail-closed gate of the
// /internal group when no auth client (and therefore no internal key) is
// configured: both routes must reject with 401.
func TestInternalRoutesRequireInternalKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := ProxyRouter(&fakeProxyService{}, nil, nil)

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/internal/heartbeat", nil),
		httptest.NewRequest(http.MethodPost, "/internal/proxy-register", strings.NewReader(`{"gameUUID":"g","playerName":"p"}`)),
	} {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Key", "anything")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		assertErrorEnvelope(t, response, http.StatusUnauthorized, codeCascadeInvalidInternalKey)
	}
}

// TestAccountRoutesAbsentWithoutAuthClient documents that the /api/auth/*
// surface only exists when an HRPAuth client is configured.
func TestAccountRoutesAbsentWithoutAuthClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := ProxyRouter(&fakeProxyService{}, nil, nil)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/auth/verify", strings.NewReader(`{}`)))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 without an auth client", response.Code)
	}
}
