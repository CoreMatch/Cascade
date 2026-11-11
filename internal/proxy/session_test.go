package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"cascade/internal/backend"
	"cascade/internal/database"
	"cascade/internal/protocol"
)

func TestSessionForwardsOpaqueFramesBothDirections(t *testing.T) {
	clientPeer, clientProxy := net.Pipe()
	backendPeer, backendProxy := net.Pipe()
	server := &Server{}
	server.cfg.ReadTimeoutSec = 0
	server.cfg.HealthIntervalSec = 30
	sess := &session{
		server: server, client: clientProxy, backend: backendProxy,
		backendClient: &backend.Client{ID: "test"},
		writeMu:       new(sync.Mutex),
	}
	sess.bound.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		sess.run(ctx)
		close(done)
	}()

	clientPacket := protocol.Packet{Type: 777, Body: []byte{1, 2, 3, 4}}
	clientWrite := make(chan error, 1)
	go func() { clientWrite <- protocol.WritePacket(clientPeer, clientPacket) }()
	gotFromClient, err := protocol.ReadPacket(backendPeer)
	if err != nil {
		t.Fatal(err)
	}
	if gotFromClient.Type != clientPacket.Type || string(gotFromClient.Body) != string(clientPacket.Body) {
		t.Fatalf("client packet changed: %+v", gotFromClient)
	}
	if err := <-clientWrite; err != nil {
		t.Fatal(err)
	}

	backendPacket := protocol.Packet{Type: 778, Body: []byte{9, 8, 7}}
	backendWrite := make(chan error, 1)
	go func() { backendWrite <- protocol.WritePacket(backendPeer, backendPacket) }()
	gotFromBackend, err := protocol.ReadPacket(clientPeer)
	if err != nil {
		t.Fatal(err)
	}
	if gotFromBackend.Type != backendPacket.Type || string(gotFromBackend.Body) != string(backendPacket.Body) {
		t.Fatalf("backend packet changed: %+v", gotFromBackend)
	}
	if err := <-backendWrite; err != nil {
		t.Fatal(err)
	}

	cancel()
	_ = clientPeer.Close()
	_ = backendPeer.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("session did not stop after cancellation")
	}
}

func TestAuthHandlerLeavesBackendCommandsUnmodified(t *testing.T) {
	writer := protocol.NewWriter()
	if err := writer.String("/start"); err != nil {
		t.Fatal(err)
	}
	sess := &session{}
	if sess.handleAuthChat(context.Background(), writer.Bytes()) {
		t.Fatal("backend-owned /start command was consumed by proxy")
	}
}

func TestBindFailureLogCarriesDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	sess := &session{
		server:        &Server{logger: log.New(&buf, "", 0)},
		backendClient: &backend.Client{ID: "backend-a"},
		sessionID:     "session-123",
		writeMu:       new(sync.Mutex),
	}

	// Backend API errors must surface HTTP status and error code.
	bindErr := fmt.Errorf("bind backend session identity: %w", &backend.APIError{
		Status: http.StatusBadGateway, Code: "invalid_token", Message: "token rejected",
	})
	sess.logBindFailure(bindErr)
	line := buf.String()
	for _, want := range []string{"session-123", "backend-a", "502", "invalid_token", "token rejected"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line missing %q: %s", want, line)
		}
	}

	// Non-API errors keep the same session/backend context.
	buf.Reset()
	sess.logBindFailure(errors.New("decode player registration: short buffer"))
	line = buf.String()
	for _, want := range []string{"session-123", "backend-a", "short buffer"} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line missing %q: %s", want, line)
		}
	}
}

// fakeAuthCache is a test double for the Redis auth-state cache.
type fakeAuthCache struct {
	uuid string
	ok   bool
}

func (f *fakeAuthCache) IsAuthenticated(string) (string, bool) { return f.uuid, f.ok }

func (f *fakeAuthCache) SetAuthenticated(string, string, time.Duration) error { return nil }

// fakeStore satisfies database.Store for restoreAuth tests; only the
// binding lookup is exercised.
type fakeStore struct {
	binding *database.PlayerBinding
}

func (f *fakeStore) GetBindingByGameUUID(string) (*database.PlayerBinding, error) {
	return f.binding, nil
}
func (f *fakeStore) GetBindingsByHAUUID(string) ([]database.PlayerBinding, error) {
	return nil, nil
}
func (f *fakeStore) CreateProxyBinding(string, string) error   { return nil }
func (f *fakeStore) ClaimBinding(string, string) error         { return nil }
func (f *fakeStore) GetLang(string) (string, error)            { return "en", nil }
func (f *fakeStore) SetLang(string, string) error              { return nil }
func (f *fakeStore) CleanupStaleProxyBindings() (int64, error) { return 0, nil }
func (f *fakeStore) Close() error                              { return nil }

func TestRestoreAuthOnlyTrustsCachedStateWithABinding(t *testing.T) {
	for _, test := range []struct {
		name       string
		cache      *fakeAuthCache
		store      *fakeStore
		wantAuth   bool
		wantHAUUID string
	}{
		{
			name:       "cached auth with an existing binding restores the session",
			cache:      &fakeAuthCache{uuid: "ha-user-1", ok: true},
			store:      &fakeStore{binding: &database.PlayerBinding{GameUUID: "game-1"}},
			wantAuth:   true,
			wantHAUUID: "ha-user-1",
		},
		{
			name:  "cached auth without a binding stays unauthenticated",
			cache: &fakeAuthCache{uuid: "ha-user-1", ok: true},
			store: &fakeStore{},
		},
		{
			name:  "no cached auth stays unauthenticated",
			cache: &fakeAuthCache{},
			store: &fakeStore{binding: &database.PlayerBinding{GameUUID: "game-1"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sess := &session{
				server:   &Server{rdb: test.cache, db: test.store},
				playerID: "game-1",
			}
			sess.restoreAuth()
			if sess.authenticated != test.wantAuth || sess.haUserUUID != test.wantHAUUID {
				t.Fatalf("restoreAuth = (authenticated=%v, uuid=%q), want (%v, %q)",
					sess.authenticated, sess.haUserUUID, test.wantAuth, test.wantHAUUID)
			}
		})
	}
}

func TestRestoreAuthWithoutRedisOrDatabaseIsANoOp(t *testing.T) {
	sess := &session{server: &Server{}, playerID: "game-1"}
	sess.restoreAuth() // must not panic
	if sess.authenticated {
		t.Fatal("session became authenticated without a cache or database")
	}
}
