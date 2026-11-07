package proxy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cascade/internal/backend"
	"cascade/internal/config"
	"cascade/internal/protocol"
)

// TestProxyBindsRegisteredIdentityOverPlaintextBackend exercises the full
// identity-binding chain over plaintext transport (contract v1.2: open/ip
// backends are served without TLS). The preamble, JWT signing, bind call,
// and Preregister-only gating must behave exactly as they do over mTLS.
func TestProxyBindsRegisteredIdentityOverPlaintextBackend(t *testing.T) {
	dir := t.TempDir()
	signingKeyPath := filepath.Join(dir, "proxy-key.pem")
	publicKey, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signingDER, err := x509.MarshalPKCS8PrivateKey(signingKey)
	if err != nil {
		t.Fatal(err)
	}
	writeProxyTestFile(t, signingKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: signingDER}), 0o600)

	controlListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer controlListener.Close()
	bindRequests := make(chan struct {
		sessionID string
		token     string
	}, 1)
	controlMux := http.NewServeMux()
	controlMux.HandleFunc("/v1/health", func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			t.Error("control-plane request arrived over TLS toward an open backend")
		}
		_, _ = io.WriteString(w, `{"backendId":"backend-plain","status":"ready"}`)
	})
	controlMux.HandleFunc("/v1/rooms", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"backendId":"backend-plain","generatedAt":"2026-11-05T00:00:00Z","rooms":[]}`)
	})
	controlMux.HandleFunc("/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || !strings.HasSuffix(r.URL.Path, "/identity") {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		sessionID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/sessions/"), "/identity")
		bindRequests <- struct {
			sessionID string
			token     string
		}{sessionID: sessionID, token: body.Token}
		w.WriteHeader(http.StatusNoContent)
	})
	controlServer := &http.Server{Handler: controlMux}
	go func() { _ = controlServer.Serve(controlListener) }()
	defer func() { _ = controlServer.Close() }()

	gameListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer gameListener.Close()
	gameResult := make(chan struct {
		sessionID      string
		preregister    protocol.Packet
		preregisterRaw []byte
		register       protocol.Packet
	}, 1)
	gameErrors := make(chan error, 1)
	go func() {
		// Plaintext game endpoint: read the CASC preamble straight off the
		// raw TCP stream — no TLS handshake happens on this path.
		conn, err := gameListener.Accept()
		if err != nil {
			gameErrors <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		sessionID, err := backend.ReadPreamble(conn)
		if err != nil {
			gameErrors <- err
			return
		}
		preregister, preregisterRaw, err := protocol.ReadPacketRaw(conn)
		if err != nil {
			gameErrors <- err
			return
		}
		if err := protocol.WritePacket(conn, protocol.Packet{Type: protocol.TypePreregister}); err != nil {
			gameErrors <- err
			return
		}
		register, err := protocol.ReadPacket(conn)
		if err != nil {
			gameErrors <- err
			return
		}
		gameResult <- struct {
			sessionID      string
			preregister    protocol.Packet
			preregisterRaw []byte
			register       protocol.Packet
		}{sessionID, preregister, preregisterRaw, register}
	}()

	cfg := config.Config{
		GameListenAddr:           "127.0.0.1:0",
		RegisterTimeoutSec:       3,
		ReadTimeoutSec:           0,
		MaxConnections:           0,
		ChatRateLimit:            0,
		DefaultBackendID:         "backend-plain",
		Backends:                 []config.BackendConfig{{ID: "backend-plain", ControlURL: "http://" + controlListener.Addr().String(), GameAddress: gameListener.Addr().String(), TrustMode: "open"}},
		ProxyIssuer:              "proxy-test",
		ProxyKeyID:               "test-key",
		ProxyPrivateKeyPath:      signingKeyPath,
		HealthIntervalSec:        1,
		RoomSnapshotIntervalSec:  60,
		PendingSessionTimeoutSec: 5,
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0), nil, nil, nil)
	if err != nil {
		t.Fatalf("NewServer without mTLS material for an open backend: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.Directory().Start(ctx)
	waitUntil(t, 3*time.Second, server.Ready)

	clientPeer, clientProxy := net.Pipe()
	sessionDone := make(chan struct{})
	go func() {
		server.handleConnection(ctx, clientProxy, "")
		close(sessionDone)
	}()

	preregisterWriter := protocol.NewWriter()
	if err := preregisterWriter.String("client"); err != nil {
		t.Fatal(err)
	}
	preregisterWriter.Int32(2)
	preregisterWriter.Int32(151)
	preregisterWriter.Int32(0)
	if err := preregisterWriter.MaybeString("ROOM1"); err != nil {
		t.Fatal(err)
	}
	preregisterPacket := preregisterWriter.Packet(protocol.TypePreregisterReceive)
	preregisterRaw := writePacketBytes(t, preregisterPacket)
	writeErr := make(chan error, 1)
	go func() { writeErr <- writeFull(clientPeer, preregisterRaw) }()
	response, err := protocol.ReadPacket(clientPeer)
	if err != nil {
		t.Fatalf("read backend preregister response: %v", err)
	}
	if response.Type != protocol.TypePreregister {
		t.Fatalf("preregister response type = %d, want %d", response.Type, protocol.TypePreregister)
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}

	registerWriter := protocol.NewWriter()
	if err := registerWriter.String("network"); err != nil {
		t.Fatal(err)
	}
	registerWriter.Raw(make([]byte, 12))
	if err := registerWriter.String("TestPlayer"); err != nil {
		t.Fatal(err)
	}
	if err := registerWriter.MaybeString(""); err != nil {
		t.Fatal(err)
	}
	if err := registerWriter.String("team"); err != nil {
		t.Fatal(err)
	}
	if err := registerWriter.String("game-uuid-plain"); err != nil {
		t.Fatal(err)
	}
	registerPacket := registerWriter.Packet(protocol.TypeRegisterPlayer)
	registerRaw := writePacketBytes(t, registerPacket)
	writeErr = make(chan error, 1)
	go func() { writeErr <- writeFull(clientPeer, registerRaw) }()

	var binding struct {
		sessionID string
		token     string
	}
	select {
	case binding = <-bindRequests:
	case err := <-gameErrors:
		t.Fatalf("test backend failed before identity binding: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("proxy did not bind session identity")
	}
	if err := <-writeErr; err != nil {
		t.Fatal(err)
	}

	var received struct {
		sessionID      string
		preregister    protocol.Packet
		preregisterRaw []byte
		register       protocol.Packet
	}
	select {
	case received = <-gameResult:
	case err := <-gameErrors:
		t.Fatalf("test backend failed reading forwarded registration: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("test backend did not receive registration")
	}
	if received.sessionID != binding.sessionID {
		t.Fatalf("preamble session ID %q differs from bound session %q", received.sessionID, binding.sessionID)
	}
	if received.preregister.Type != preregisterPacket.Type || !bytes.Equal(received.preregisterRaw, preregisterRaw) {
		t.Fatal("proxy changed the initial preregister frame")
	}
	if received.register.Type != registerPacket.Type || !bytes.Equal(received.register.Body, registerPacket.Body) {
		t.Fatal("proxy changed or failed to forward RegisterPlayer")
	}

	parts := strings.Split(binding.token, ".")
	if len(parts) != 3 {
		t.Fatalf("bound credential has %d JWT segments", len(parts))
	}
	unsigned := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(publicKey, []byte(unsigned), signature) {
		t.Fatal("backend received an invalid proxy JWT signature")
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims tokenClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != "proxy-test" || claims.Audience != "backend-plain" ||
		claims.SessionID != received.sessionID || claims.Subject != "game-uuid-plain" {
		t.Fatalf("JWT claims do not match the bound game session: %+v", claims)
	}

	_ = clientPeer.Close()
	select {
	case <-sessionDone:
	case <-time.After(time.Second):
		t.Fatal("proxy session did not close after client disconnect")
	}
}

// startJumpBackend starts a minimal plaintext fake backend (control + game) and
// returns its control URL, game address, and a channel that receives the
// forwarded Preregister packet when a game connection lands on it.
func startJumpBackend(t *testing.T, backendID, roomsJSON string) (string, string, chan protocol.Packet) {
	t.Helper()
	gotConn := make(chan protocol.Packet, 1)

	controlListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controlListener.Close() })
	controlMux := http.NewServeMux()
	controlMux.HandleFunc("/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"backendId":"`+backendID+`","status":"ready"}`)
	})
	controlMux.HandleFunc("/v1/rooms", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"backendId":"`+backendID+`","generatedAt":"2026-11-08T00:00:00Z","rooms":`+roomsJSON+`}`)
	})
	controlMux.HandleFunc("/v1/sessions/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	controlServer := &http.Server{Handler: controlMux}
	go func() { _ = controlServer.Serve(controlListener) }()
	t.Cleanup(func() { _ = controlServer.Close() })

	gameListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gameListener.Close() })
	go func() {
		conn, err := gameListener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := backend.ReadPreamble(conn); err != nil {
			return
		}
		preregister, _, err := protocol.ReadPacketRaw(conn)
		if err != nil {
			return
		}
		gotConn <- preregister
		time.Sleep(200 * time.Millisecond)
	}()

	return "http://" + controlListener.Addr().String(), gameListener.Addr().String(), gotConn
}

// TestJumpPortRoutesToForcedBackend verifies the lobby-jump routing: a
// connection handled with a forced backend ID (i.e. landing on a jump port)
// routes to that backend even though the Preregister room code is empty (which
// would otherwise route to the default/lobby backend).
func TestJumpPortRoutesToForcedBackend(t *testing.T) {
	dir := t.TempDir()
	signingKeyPath := filepath.Join(dir, "proxy-key.pem")
	_, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signingDER, err := x509.MarshalPKCS8PrivateKey(signingKey)
	if err != nil {
		t.Fatal(err)
	}
	writeProxyTestFile(t, signingKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: signingDER}), 0o600)

	lobbyControl, lobbyGame, lobbyConn := startJumpBackend(t, "lobby", "[]")
	targetControl, targetGame, targetConn := startJumpBackend(t, "target", "[]")

	cfg := config.Config{
		GameListenAddr:           "127.0.0.1:0",
		RegisterTimeoutSec:       3,
		ReadTimeoutSec:           0,
		MaxConnections:           0,
		ChatRateLimit:            0,
		DefaultBackendID:         "lobby",
		Backends: []config.BackendConfig{
			{ID: "lobby", ControlURL: lobbyControl, GameAddress: lobbyGame, TrustMode: "open"},
			{ID: "target", ControlURL: targetControl, GameAddress: targetGame, TrustMode: "open", JumpPort: 5201},
		},
		ProxyIssuer:              "proxy-test",
		ProxyKeyID:               "test-key",
		ProxyPrivateKeyPath:      signingKeyPath,
		HealthIntervalSec:        1,
		RoomSnapshotIntervalSec:  60,
		PendingSessionTimeoutSec: 5,
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.Directory().Start(ctx)
	waitUntil(t, 3*time.Second, server.Ready)
	waitUntil(t, 3*time.Second, func() bool { return server.Directory().BackendHealthy("target") })

	// Empty room code would normally route to the default (lobby); the forced
	// backend ID (jump port) must override that and land on "target".
	clientPeer, clientProxy := net.Pipe()
	defer clientPeer.Close()
	go server.handleConnection(ctx, clientProxy, "target")

	preregisterWriter := protocol.NewWriter()
	if err := preregisterWriter.String("client"); err != nil {
		t.Fatal(err)
	}
	preregisterWriter.Int32(2)
	preregisterWriter.Int32(151)
	preregisterWriter.Int32(0)
	if err := preregisterWriter.MaybeString(""); err != nil { // empty room code
		t.Fatal(err)
	}
	preregisterRaw := writePacketBytes(t, preregisterWriter.Packet(protocol.TypePreregisterReceive))
	go func() { _ = writeFull(clientPeer, preregisterRaw) }()

	select {
	case <-targetConn:
		// routed to the forced target backend — correct
	case <-lobbyConn:
		t.Fatal("jump connection routed to the lobby instead of the forced target backend")
	case <-time.After(3 * time.Second):
		t.Fatal("no backend received the jump connection")
	}
}

// TestCommandGotoSendsReconnectToJumpPort verifies /goto resolves a room code to
// its backend and sends the client a PACKET_RECONNECT_TO aimed at the backend's
// Cascade jump port (public host + jump port), never the backend's real address.
func TestCommandGotoSendsReconnectToJumpPort(t *testing.T) {
	dir := t.TempDir()
	signingKeyPath := filepath.Join(dir, "proxy-key.pem")
	_, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signingDER, err := x509.MarshalPKCS8PrivateKey(signingKey)
	if err != nil {
		t.Fatal(err)
	}
	writeProxyTestFile(t, signingKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: signingDER}), 0o600)

	roomsJSON := `[{"code":"GAME1","title":"Game One","maxPlayers":8,"players":2,"inGame":false}]`
	targetControl, targetGame, _ := startJumpBackend(t, "target", roomsJSON)

	cfg := config.Config{
		GameListenAddr:           "127.0.0.1:0",
		RegisterTimeoutSec:       3,
		ReadTimeoutSec:           0,
		MaxConnections:           0,
		ChatRateLimit:            0,
		DefaultBackendID:         "target",
		PublicGameHost:           "cascade.example",
		Backends: []config.BackendConfig{
			{ID: "target", ControlURL: targetControl, GameAddress: targetGame, TrustMode: "open", JumpPort: 5201},
		},
		ProxyIssuer:              "proxy-test",
		ProxyKeyID:               "test-key",
		ProxyPrivateKeyPath:      signingKeyPath,
		HealthIntervalSec:        1,
		RoomSnapshotIntervalSec:  60,
		PendingSessionTimeoutSec: 5,
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.Directory().Start(ctx)
	// target 是默认后端: Ready 即 target 健康 (RouteRoom 需要), 再等快照里有房间.
	waitUntil(t, 5*time.Second, func() bool {
		return server.Ready() && len(server.Directory().SnapshotRooms()) > 0
	})

	clientPeer, clientProxy := net.Pipe()
	defer clientPeer.Close()
	sess := &session{
		server:  server,
		client:  clientProxy,
		writeMu: &sync.Mutex{},
		locale:  "en",
	}

	// net.Pipe writes block until read, so run the command concurrently and
	// drain the confirmation + reconnect packets below.
	go sess.commandGoto([]string{"GAME1"})

	// commandGoto first replies with a chat confirmation, then the 178. Read
	// until the reconnect packet arrives.
	_ = clientPeer.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		packet, err := protocol.ReadPacket(clientPeer)
		if err != nil {
			t.Fatalf("read client packet: %v", err)
		}
		if packet.Type != protocol.TypeReconnectTo {
			continue
		}
		r := protocol.NewReader(packet.Body)
		_, _ = r.Byte()
		_, _ = r.Int32()
		_, _ = r.Bool()
		_, _ = r.Int32()
		target, err := r.String()
		if err != nil {
			t.Fatal(err)
		}
		if target != "cascade.example:5201" {
			t.Fatalf("reconnect target = %q, want cascade.example:5201", target)
		}
		return
	}
}
