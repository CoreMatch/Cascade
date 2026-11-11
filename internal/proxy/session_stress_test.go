package proxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cascade/internal/backend"
	"cascade/internal/config"
	"cascade/internal/protocol"
)

// TestConcurrentSessionBindingStress drives many full client sessions
// (Preregister -> RegisterPlayer -> identity bind) through the proxy
// concurrently against a fake plaintext backend. Every session must arrive
// at the backend bound; any loss fails the test. Throughput and allocation
// numbers are logged as the regression baseline recorded in
// knowledge/appendix-stress-baseline.md.
func TestConcurrentSessionBindingStress(t *testing.T) {
	const (
		totalSessions = 256
		concurrency   = 32
	)

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

	var binds, completed atomic.Int64
	controlMux := http.NewServeMux()
	controlMux.HandleFunc("/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"backendId":"backend-stress","status":"ready"}`)
	})
	controlMux.HandleFunc("/v1/rooms", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"backendId":"backend-stress","generatedAt":"2026-11-05T00:00:00Z","rooms":[]}`)
	})
	controlMux.HandleFunc("/v1/sessions/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || !strings.HasSuffix(r.URL.Path, "/identity") {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		binds.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})
	controlServer := httptest.NewServer(controlMux)
	defer controlServer.Close()

	gameListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer gameListener.Close()
	go func() {
		for {
			conn, err := gameListener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
				if _, err := backend.ReadPreamble(conn); err != nil {
					return
				}
				if _, _, err := protocol.ReadPacketRaw(conn); err != nil {
					return
				}
				if err := protocol.WritePacket(conn, protocol.Packet{Type: protocol.TypePreregister}); err != nil {
					return
				}
				if _, err := protocol.ReadPacket(conn); err != nil {
					return
				}
				completed.Add(1)
			}(conn)
		}
	}()

	cfg := config.Config{
		GameListenAddr:           "127.0.0.1:0",
		RegisterTimeoutSec:       5,
		DefaultBackendID:         "backend-stress",
		Backends:                 []config.BackendConfig{{ID: "backend-stress", ControlURL: controlServer.URL, GameAddress: gameListener.Addr().String(), TrustMode: "open"}},
		ProxyIssuer:              "proxy-stress",
		ProxyKeyID:               "stress-key",
		ProxyPrivateKeyPath:      signingKeyPath,
		HealthIntervalSec:        1,
		RoomSnapshotIntervalSec:  60,
		PendingSessionTimeoutSec: 5,
	}
	server, err := NewServer(cfg, log.New(io.Discard, "", 0), nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.Directory().Start(ctx)
	waitUntil(t, 3*time.Second, server.Ready)

	preregisterWriter := protocol.NewWriter()
	if err := preregisterWriter.String("client"); err != nil {
		t.Fatal(err)
	}
	preregisterWriter.Int32(2)
	preregisterWriter.Int32(151)
	preregisterWriter.Int32(0)
	if err := preregisterWriter.MaybeString(""); err != nil {
		t.Fatal(err)
	}
	preregisterRaw := writePacketBytes(t, preregisterWriter.Packet(protocol.TypePreregisterReceive))

	runSession := func(id int) error {
		registerWriter := protocol.NewWriter()
		if err := registerWriter.String("network"); err != nil {
			return err
		}
		registerWriter.Raw(make([]byte, 12))
		if err := registerWriter.String(fmt.Sprintf("player-%04d", id)); err != nil {
			return err
		}
		if err := registerWriter.MaybeString(""); err != nil {
			return err
		}
		if err := registerWriter.String("team"); err != nil {
			return err
		}
		if err := registerWriter.String(fmt.Sprintf("game-uuid-%04d", id)); err != nil {
			return err
		}
		registerRaw := writePacketBytes(t, registerWriter.Packet(protocol.TypeRegisterPlayer))

		clientPeer, clientProxy := net.Pipe()
		sessionDone := make(chan struct{})
		go func() {
			server.handleConnection(ctx, clientProxy, "")
			close(sessionDone)
		}()
		defer clientPeer.Close()

		if err := writeFull(clientPeer, preregisterRaw); err != nil {
			return fmt.Errorf("session %d: write preregister: %w", id, err)
		}
		response, err := protocol.ReadPacket(clientPeer)
		if err != nil {
			return fmt.Errorf("session %d: read preregister ack: %w", id, err)
		}
		if response.Type != protocol.TypePreregister {
			return fmt.Errorf("session %d: preregister ack type = %d", id, response.Type)
		}
		if err := writeFull(clientPeer, registerRaw); err != nil {
			return fmt.Errorf("session %d: write register: %w", id, err)
		}
		select {
		case <-sessionDone:
		case <-time.After(15 * time.Second):
			return fmt.Errorf("session %d: proxy session did not finish", id)
		}
		return nil
	}

	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	var goroutinePeak atomic.Int64
	goroutinePeak.Store(int64(runtime.NumGoroutine()))

	start := time.Now()
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, concurrency)
	errs := make(chan error, totalSessions)
	for id := 0; id < totalSessions; id++ {
		wg.Add(1)
		semaphore <- struct{}{}
		go func(id int) {
			defer wg.Done()
			defer func() { <-semaphore }()
			if goroutines := int64(runtime.NumGoroutine()); goroutines > goroutinePeak.Load() {
				goroutinePeak.Store(goroutines)
			}
			if err := runSession(id); err != nil {
				errs <- err
			}
		}(id)
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(errs)

	var failures []error
	for err := range errs {
		failures = append(failures, err)
	}
	if len(failures) > 0 {
		limit := len(failures)
		if limit > 5 {
			limit = 5
		}
		t.Fatalf("%d/%d sessions failed, first errors: %v", len(failures), totalSessions, failures[:limit])
	}
	if got := binds.Load(); got != totalSessions {
		t.Fatalf("identity binds = %d, want %d", got, totalSessions)
	}
	if got := completed.Load(); got != totalSessions {
		t.Fatalf("backend-completed sessions = %d, want %d", got, totalSessions)
	}

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	t.Logf("stress baseline: %d sessions at concurrency %d in %s (%.1f sessions/s)",
		totalSessions, concurrency, elapsed.Round(time.Millisecond), float64(totalSessions)/elapsed.Seconds())
	t.Logf("stress baseline memory: %d KiB allocated, %d mallocs, %d goroutines at peak",
		(after.TotalAlloc-before.TotalAlloc)/1024, after.Mallocs-before.Mallocs, goroutinePeak.Load())
}
