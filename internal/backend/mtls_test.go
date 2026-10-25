package backend

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"cascade/internal/config"
)

func TestBackendClientUsesMTLSForControlAndGameTransports(t *testing.T) {
	caCert, caKey, caPEM := makeTestCA(t)
	serverCert := makeTestLeaf(t, caCert, caKey, "localhost", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	clientCert := makeTestLeaf(t, caCert, caKey, "proxy", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})

	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	clientCertPath := filepath.Join(dir, "client.pem")
	clientKeyPath := filepath.Join(dir, "client-key.pem")
	writePEM(t, caPath, caPEM, 0o600)
	writePEM(t, clientCertPath, clientCert.certPEM, 0o600)
	writePEM(t, clientKeyPath, clientCert.keyPEM, 0o600)

	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	serverTLS := &tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert.pair},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots,
	}
	control := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			_, _ = fmt.Fprint(w, `{"backendId":"mtls-test","status":"ready"}`)
			return
		}
		if r.URL.Path == "/v1/rooms" {
			_, _ = fmt.Fprint(w, `{"backendId":"mtls-test","generatedAt":"2026-10-24T00:00:00Z","rooms":[]}`)
			return
		}
		http.NotFound(w, r)
	}))
	control.TLS = serverTLS.Clone()
	control.StartTLS()
	defer control.Close()

	gameListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gameTLSListener := tls.NewListener(gameListener, serverTLS.Clone())
	defer gameTLSListener.Close()
	preambleResult := make(chan string, 1)
	go func() {
		conn, err := gameTLSListener.Accept()
		if err != nil {
			preambleResult <- "accept error: " + err.Error()
			return
		}
		defer conn.Close()
		sessionID, err := ReadPreamble(conn)
		if err != nil {
			preambleResult <- "preamble error: " + err.Error()
			return
		}
		preambleResult <- sessionID
	}()

	client, err := NewClient(config.BackendConfig{
		ID: "mtls-test", ControlURL: control.URL,
		GameAddress: gameListener.Addr().String(),
	}, clientCertPath, clientKeyPath, caPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Health(ctx); err != nil {
		t.Fatalf("mTLS health call failed: %v", err)
	}
	snapshot, err := client.Rooms(ctx)
	if err != nil {
		t.Fatalf("mTLS room snapshot failed: %v", err)
	}
	if snapshot.BackendID != "mtls-test" || snapshot.Rooms == nil {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	conn, err := client.DialGame(ctx)
	if err != nil {
		t.Fatalf("mTLS game connection failed: %v", err)
	}
	sessionID, err := NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePreamble(conn, sessionID); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if got := <-preambleResult; got != sessionID {
		t.Fatalf("backend received session ID %q, want %q", got, sessionID)
	}
}

type testLeaf struct {
	pair    tls.Certificate
	certPEM []byte
	keyPEM  []byte
}

func makeTestCA(t *testing.T) (*x509.Certificate, ed25519.PrivateKey, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Cascade test CA"},
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

func makeTestLeaf(t *testing.T, ca *x509.Certificate, caKey ed25519.PrivateKey, name string, usage []x509.ExtKeyUsage) testLeaf {
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
	if name == "localhost" {
		template.DNSNames = []string{"localhost"}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
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
	return testLeaf{pair: pair, certPEM: certPEM, keyPEM: keyPEM}
}

func writePEM(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}
