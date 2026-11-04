package backend

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cascade/internal/config"
)

// plainControlServer serves /v1/health and /v1/rooms over plaintext HTTP and
// records whether any request arrived over TLS (it must not).
func plainControlServer(t *testing.T, backendID string) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	var sawTLS atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			sawTLS.Store(true)
		}
		switch r.URL.Path {
		case "/v1/health":
			_, _ = fmt.Fprintf(w, `{"backendId":%q,"status":"ready"}`, backendID)
		case "/v1/rooms":
			_, _ = fmt.Fprintf(w, `{"backendId":%q,"generatedAt":"2026-10-24T00:00:00Z","rooms":[]}`, backendID)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &sawTLS
}

// plainGameListener accepts one raw TCP game connection and reads the CASC
// preamble directly from the wire — a TLS client would fail the magic check.
func plainGameListener(t *testing.T) (net.Listener, chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	result := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			result <- "accept error: " + err.Error()
			return
		}
		defer conn.Close()
		sessionID, err := ReadPreamble(conn)
		if err != nil {
			result <- "preamble error: " + err.Error()
			return
		}
		result <- sessionID
	}()
	return listener, result
}

func TestBackendClientUsesPlaintextForOpenAndIPBackends(t *testing.T) {
	for _, trustMode := range []string{TrustOpen, TrustIP} {
		t.Run("trust="+trustMode, func(t *testing.T) {
			control, sawTLS := plainControlServer(t, "plain-test")
			game, preambleResult := plainGameListener(t)

			// No certificate material at all: plaintext backends need none.
			client, err := NewClient(config.BackendConfig{
				ID: "plain-test", ControlURL: control.URL,
				GameAddress: game.Addr().String(), TrustMode: trustMode,
			}, "", "", "")
			if err != nil {
				t.Fatalf("NewClient with %q backend: %v", trustMode, err)
			}
			if client.Transport != TransportPlain {
				t.Fatalf("resolved transport = %s, want plain", client.Transport)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := client.Health(ctx); err != nil {
				t.Fatalf("plaintext health call failed: %v", err)
			}
			snapshot, err := client.Rooms(ctx)
			if err != nil {
				t.Fatalf("plaintext room snapshot failed: %v", err)
			}
			if snapshot.BackendID != "plain-test" || snapshot.Rooms == nil {
				t.Fatalf("unexpected snapshot: %+v", snapshot)
			}
			if sawTLS.Load() {
				t.Fatal("control-plane request used TLS toward an open/ip backend")
			}

			conn, err := client.DialGame(ctx)
			if err != nil {
				t.Fatalf("plaintext game connection failed: %v", err)
			}
			sessionID, err := NewSessionID()
			if err != nil {
				t.Fatal(err)
			}
			if err := WritePreamble(conn, sessionID); err != nil {
				t.Fatalf("write preamble over plaintext: %v", err)
			}
			_ = conn.Close()
			if got := <-preambleResult; got != sessionID {
				t.Fatalf("backend received session ID %q, want %q", got, sessionID)
			}
		})
	}
}

func TestBackendClientAutoDerivesMTLSForPinnedBackend(t *testing.T) {
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
	var sawClientCert atomic.Bool
	control := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			sawClientCert.Store(true)
		}
		if r.URL.Path == "/v1/health" {
			_, _ = fmt.Fprint(w, `{"backendId":"pinned-test","status":"ready"}`)
			return
		}
		http.NotFound(w, r)
	}))
	control.TLS = serverTLS.Clone()
	control.StartTLS()
	defer control.Close()

	// tls_mode left empty: the pinned declaration alone must select mTLS.
	client, err := NewClient(config.BackendConfig{
		ID: "pinned-test", ControlURL: control.URL,
		GameAddress: "127.0.0.1:1", TrustMode: TrustPinned,
	}, clientCertPath, clientKeyPath, caPath)
	if err != nil {
		t.Fatal(err)
	}
	if client.Transport != TransportMTLS {
		t.Fatalf("resolved transport = %s, want mtls", client.Transport)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Health(ctx); err != nil {
		t.Fatalf("mTLS health call failed: %v", err)
	}
	if !sawClientCert.Load() {
		t.Fatal("proxy did not present its client certificate toward a pinned backend")
	}
}

func TestBackendClientFailsClosedWhenDeclarationUnparseable(t *testing.T) {
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
	control := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			_, _ = fmt.Fprint(w, `{"backendId":"fail-closed","status":"ready"}`)
			return
		}
		http.NotFound(w, r)
	}))
	control.TLS = &tls.Config{
		MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{serverCert.pair},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots,
	}
	control.StartTLS()
	defer control.Close()

	// An unparseable declaration must resolve to the safest transport (mTLS),
	// never to plaintext.
	client, err := NewClient(config.BackendConfig{
		ID: "fail-closed", ControlURL: control.URL,
		GameAddress: "127.0.0.1:1", TrustMode: "wide-open",
	}, clientCertPath, clientKeyPath, caPath)
	if err != nil {
		t.Fatal(err)
	}
	if client.Transport != TransportMTLS {
		t.Fatalf("resolved transport = %s, want mtls (fail closed)", client.Transport)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Health(ctx); err != nil {
		t.Fatalf("mTLS health call failed: %v", err)
	}
}

func TestNewClientRejectsSchemeAndTransportMismatch(t *testing.T) {
	for _, test := range []struct {
		name       string
		config     config.BackendConfig
		wantSubstr string
	}{
		{
			name: "plaintext transport rejects https control URL",
			config: config.BackendConfig{
				ID: "backend", ControlURL: "https://backend.example", GameAddress: "backend.example:5123",
				TrustMode: TrustOpen,
			},
			wantSubstr: "must use http",
		},
		{
			name: "mtls transport rejects http control URL",
			config: config.BackendConfig{
				ID: "backend", ControlURL: "http://backend.example", GameAddress: "backend.example:5123",
				TrustMode: TrustPinned,
			},
			wantSubstr: "must use https",
		},
		{
			name: "mtls transport requires certificate material",
			config: config.BackendConfig{
				ID: "backend", ControlURL: "https://backend.example", GameAddress: "backend.example:5123",
				TrustMode: TrustPinned,
			},
			wantSubstr: "certificate, key, and CA paths are required",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewClient(test.config, "", "", "")
			if err == nil || !strings.Contains(err.Error(), test.wantSubstr) {
				t.Fatalf("NewClient error = %v, want substring %q", err, test.wantSubstr)
			}
		})
	}
}

// TestNewDirectoryAcceptsPlaintextBackendWithoutCertMaterial proves that a
// deployment whose backends all declare open/ip trust no longer needs any
// mTLS material at startup.
func TestNewDirectoryAcceptsPlaintextBackendWithoutCertMaterial(t *testing.T) {
	control, _ := plainControlServer(t, "dir-plain")
	directory, err := NewDirectory(config.Config{
		DefaultBackendID: "dir-plain",
		Backends: []config.BackendConfig{{
			ID: "dir-plain", ControlURL: control.URL, GameAddress: "127.0.0.1:1",
			TrustMode: TrustOpen, Prefixes: []string{"GW"},
		}},
		HealthIntervalSec: 5, RoomSnapshotIntervalSec: 5,
	})
	if err != nil {
		t.Fatalf("NewDirectory with a plaintext backend and no cert material: %v", err)
	}
	statuses := directory.Statuses()
	if len(statuses) != 1 || statuses[0].Transport != "plain" {
		t.Fatalf("directory statuses = %+v, want one backend on plain transport", statuses)
	}
}
