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
		server.handleConnection(ctx, clientProxy)
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
