package backend

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rwgin/internal/config"
)

func TestNewClientRequiresHTTPSControlURL(t *testing.T) {
	_, err := NewClient(config.BackendConfig{
		ID: "backend", ControlURL: "http://backend.example", GameAddress: "backend.example:5123",
	}, "client.crt", "client.key", "ca.crt")
	if err == nil {
		t.Fatal("expected non-HTTPS control URL to be rejected")
	}
}

func TestRoomsRejectsIncompleteSnapshotsAndAcceptsEmptyArrays(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		wantErr bool
	}{
		{
			name:    "valid empty snapshot",
			payload: `{"backendId":"backend-a","generatedAt":"2026-10-25T00:00:00Z","rooms":[]}`,
		},
		{
			name:    "missing room list",
			payload: `{"backendId":"backend-a","generatedAt":"2026-10-25T00:00:00Z"}`,
			wantErr: true,
		},
		{
			name:    "missing generation time",
			payload: `{"backendId":"backend-a","rooms":[]}`,
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, test.payload)
			}))
			defer server.Close()
			client := &Client{
				ID:         "backend-a",
				ControlURL: server.URL,
				http:       server.Client(),
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			snapshot, err := client.Rooms(ctx)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected incomplete snapshot to be rejected")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Rooms == nil || len(snapshot.Rooms) != 0 {
				t.Fatalf("empty room list was not preserved as an array: %#v", snapshot.Rooms)
			}
		})
	}
}

func TestTLSConfigRejectsOpenPrivateKeyPermissions(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(keyPath, []byte("unused"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := makeTLSConfig("cert.pem", keyPath, "ca.pem"); err == nil {
		t.Fatal("expected group-readable private key to be rejected")
	}
}
