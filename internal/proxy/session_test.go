package proxy

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"rwgin/internal/backend"
	"rwgin/internal/protocol"
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
