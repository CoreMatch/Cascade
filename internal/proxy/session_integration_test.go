package proxy

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cascade/internal/backend"
	"cascade/internal/config"
	"cascade/internal/protocol"
)

func TestProxyBindsRegisteredIdentityBeforeForwarding(t *testing.T) {
	caCert, caKey, caPEM := makeProxyTestCA(t)
	serverCert := makeProxyTestCertificate(t, caCert, caKey, "127.0.0.1", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	clientCert := makeProxyTestCertificate(t, caCert, caKey, "proxy", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})

	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	clientCertPath := filepath.Join(dir, "client.pem")
	clientKeyPath := filepath.Join(dir, "client-key.pem")
	signingKeyPath := filepath.Join(dir, "proxy-key.pem")
	writeProxyTestFile(t, caPath, caPEM, 0o600)
	writeProxyTestFile(t, clientCertPath, clientCert.certPEM, 0o600)
	writeProxyTestFile(t, clientKeyPath, clientCert.keyPEM, 0o600)

	publicKey, signingKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signingDER, err := x509.MarshalPKCS8PrivateKey(signingKey)
	if err != nil {
		t.Fatal(err)
	}
	writeProxyTestFile(t, signingKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: signingDER}), 0o600)

	clientRoots := x509.NewCertPool()
	clientRoots.AddCert(caCert)
	serverTLS := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert.pair},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientRoots,
	}

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
	controlMux.HandleFunc("/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"backendId":"backend-a","status":"ready"}`)
	})
	controlMux.HandleFunc("/v1/rooms", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"backendId":"backend-a","generatedAt":"2026-10-25T00:00:00Z","rooms":[]}`)
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
	go func() {
		_ = controlServer.Serve(tls.NewListener(controlListener, serverTLS.Clone()))
	}()
	defer func() { _ = controlServer.Close() }()

	gameListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gameTLSListener := tls.NewListener(gameListener, serverTLS.Clone())
	defer gameTLSListener.Close()
	gameResult := make(chan struct {
		sessionID      string
		preregister    protocol.Packet
		preregisterRaw []byte
		register       protocol.Packet
	}, 1)
	gameErrors := make(chan error, 1)
	go func() {
		conn, err := gameTLSListener.Accept()
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
		DefaultBackendID:         "backend-a",
		Backends:                 []config.BackendConfig{{ID: "backend-a", ControlURL: "https://" + controlListener.Addr().String(), GameAddress: gameListener.Addr().String()}},
		ProxyIssuer:              "proxy-test",
		ProxyKeyID:               "test-key",
		ProxyPrivateKeyPath:      signingKeyPath,
		BackendClientCert:        clientCertPath,
		BackendClientKey:         clientKeyPath,
		BackendCAPool:            caPath,
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
	if err := registerWriter.String("game-uuid-123"); err != nil {
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
	if claims.Issuer != "proxy-test" || claims.Audience != "backend-a" ||
		claims.SessionID != received.sessionID || claims.Subject != "game-uuid-123" {
		t.Fatalf("JWT claims do not match the bound game session: %+v", claims)
	}

	_ = clientPeer.Close()
	select {
	case <-sessionDone:
	case <-time.After(time.Second):
		t.Fatal("proxy session did not close after client disconnect")
	}
}

func writePacketBytes(t *testing.T, packet protocol.Packet) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := protocol.WritePacket(&buffer, packet); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func waitUntil(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition did not become true before timeout")
}

type proxyTestCertificate struct {
	pair    tls.Certificate
	certPEM []byte
	keyPEM  []byte
}

func makeProxyTestCA(t *testing.T) (*x509.Certificate, ed25519.PrivateKey, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Cascade proxy test CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, privateKey, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func makeProxyTestCertificate(t *testing.T, ca *x509.Certificate, caKey ed25519.PrivateKey, name string, usage []x509.ExtKeyUsage) proxyTestCertificate {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(int64(len(name) + 10)),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usage,
	}
	if name == "127.0.0.1" {
		template.IPAddresses = []net.IP{net.ParseIP(name)}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, publicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return proxyTestCertificate{pair: pair, certPEM: certPEM, keyPEM: keyPEM}
}

func writeProxyTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}
