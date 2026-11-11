package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cascade/internal/backend"
	"cascade/internal/config"
	"cascade/internal/protocol"

	mcacascade "github.com/CoreMatch/MCA-Cascade"
)

// fakeRouter is a matchRouter test double: records calls, returns canned
// results. It never does I/O.
type fakeRouter struct {
	calls    atomic.Int32
	decision mcacascade.Decision
	err      error
}

func (f *fakeRouter) Route(ctx context.Context, userUUID string) (mcacascade.Decision, error) {
	f.calls.Add(1)
	return f.decision, f.err
}

// lockedBuffer is a goroutine-safe log sink: the routing goroutine writes
// through it while the test goroutine reads it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newTestDirectory builds a backend directory without starting probes; all
// backends stay unhealthy until Start is called with live control servers.
func newTestDirectory(t *testing.T, backends []config.BackendConfig) *backend.Directory {
	t.Helper()
	cfg := config.Config{
		DefaultBackendID:        "lobby",
		Backends:                backends,
		HealthIntervalSec:       1,
		RoomSnapshotIntervalSec: 60,
	}
	dir, err := backend.NewDirectory(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func dummyBackends() []config.BackendConfig {
	return []config.BackendConfig{
		{ID: "lobby", ControlURL: "http://127.0.0.1:1", GameAddress: "127.0.0.1:2", TrustMode: "open"},
		{ID: "sub-a", ControlURL: "http://127.0.0.1:3", GameAddress: "127.0.0.1:4", TrustMode: "open", JumpPort: 5299},
	}
}

func TestNewMCARouterDisabledIsNil(t *testing.T) {
	dir := newTestDirectory(t, dummyBackends())
	router, err := newMCARouter(config.Config{}, dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if router != nil {
		t.Fatal("disabled mca block must produce a nil router (zero regression)")
	}
}

func TestNewMCARouterConfigErrors(t *testing.T) {
	valid := config.MCAConfig{
		Enabled: true,
		BaseURL: "https://arena.example",
		Token:   "tok",
		Targets: []config.MCATargetConfig{{Target: "10.0.0.5:5124", Backend: "sub-a"}},
	}
	cases := []struct {
		name    string
		mutate  func(*config.MCAConfig)
		wantSub string
	}{
		{"missing base url", func(c *config.MCAConfig) { c.BaseURL = "" }, "base_url"},
		{"missing token", func(c *config.MCAConfig) { c.Token = "" }, "token"},
		{"bad target", func(c *config.MCAConfig) { c.Targets[0].Target = "no-port" }, "host:port"},
		{"empty backend id", func(c *config.MCAConfig) { c.Targets[0].Backend = " " }, "empty backend"},
		{"unknown backend", func(c *config.MCAConfig) { c.Targets[0].Backend = "ghost" }, "unknown backend"},
		{"duplicate target", func(c *config.MCAConfig) {
			c.Targets = append(c.Targets, config.MCATargetConfig{Target: "10.0.0.5:5124", Backend: "lobby"})
		}, "duplicate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mca := valid
			// Deep copy: subtests must not mutate the shared backing array.
			mca.Targets = append([]config.MCATargetConfig(nil), valid.Targets...)
			tc.mutate(&mca)
			cfg := config.Config{MCA: mca}
			dir := newTestDirectory(t, dummyBackends())
			_, err := newMCARouter(cfg, dir, log.New(io.Discard, "", 0))
			if err == nil {
				t.Fatal("expected startup failure, got nil (misconfiguration must be explicit)")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestNewMCARouterValid(t *testing.T) {
	cfg := config.Config{MCA: config.MCAConfig{
		Enabled: true, BaseURL: "https://arena.example", Token: "tok",
		Targets: []config.MCATargetConfig{{Target: " Sub.Example:5124 ", Backend: "sub-a"}},
	}}
	dir := newTestDirectory(t, dummyBackends())
	router, err := newMCARouter(cfg, dir, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if router == nil {
		t.Fatal("enabled valid config must produce a router")
	}
}

func newRouteTestSession(t *testing.T, dir *backend.Directory, router matchRouter, logBuf *lockedBuffer) (*session, net.Conn) {
	t.Helper()
	lobbyClient, ok := dir.Client("lobby")
	if !ok {
		t.Fatal("lobby backend missing")
	}
	cfg := config.Config{
		DefaultBackendID: "lobby",
		PublicGameHost:   "cascade.test",
		Backends: []config.BackendConfig{
			{ID: "lobby", JumpPort: 5298},
			{ID: "sub-a", JumpPort: 5299},
		},
	}
	clientPeer, clientProxy := net.Pipe()
	t.Cleanup(func() { _ = clientPeer.Close() })
	sess := &session{
		server: &Server{
			cfg:       cfg,
			logger:    log.New(logBuf, "", 0),
			directory: dir,
			mcaRouter: router,
		},
		client:        clientProxy,
		backendClient: lobbyClient,
		sessionID:     "session-mca-1",
		playerID:      "player-1",
		haUserUUID:    "ha-uuid-1",
		authenticated: true,
		locale:        "en",
		writeMu:       &sync.Mutex{},
	}
	return sess, clientPeer
}

func TestMaybeRouteToMatchGuards(t *testing.T) {
	dir := newTestDirectory(t, dummyBackends())

	t.Run("nil router", func(t *testing.T) {
		var buf lockedBuffer
		sess, _ := newRouteTestSession(t, dir, nil, &buf)
		sess.maybeRouteToMatch(context.Background())
		if sess.routeOnce.Load() {
			t.Fatal("nil router must not consume the one-shot slot")
		}
	})

	t.Run("guest", func(t *testing.T) {
		var buf lockedBuffer
		router := &fakeRouter{}
		sess, _ := newRouteTestSession(t, dir, router, &buf)
		sess.authenticated, sess.haUserUUID = false, ""
		sess.maybeRouteToMatch(context.Background())
		if router.calls.Load() != 0 {
			t.Fatal("guests have no queryable identity; no query must be sent")
		}
	})

	t.Run("non-default backend", func(t *testing.T) {
		var buf lockedBuffer
		router := &fakeRouter{}
		sess, _ := newRouteTestSession(t, dir, router, &buf)
		subClient, _ := dir.Client("sub-a")
		sess.backendClient = subClient
		sess.maybeRouteToMatch(context.Background())
		if router.calls.Load() != 0 {
			t.Fatal("players not on the lobby backend must not be queried (loop prevention)")
		}
	})

	t.Run("once per session", func(t *testing.T) {
		var buf lockedBuffer
		router := &fakeRouter{}
		sess, _ := newRouteTestSession(t, dir, router, &buf)
		sess.maybeRouteToMatch(context.Background())
		sess.maybeRouteToMatch(context.Background())
		waitUntil(t, 2*time.Second, func() bool { return router.calls.Load() >= 1 })
		time.Sleep(50 * time.Millisecond) // allow a (buggy) second call to surface
		if router.calls.Load() != 1 {
			t.Fatalf("queries = %d, want exactly 1 per session", router.calls.Load())
		}
	})
}

// readPacketWithin reads one packet from the peer or fails after the deadline.
func readPacketWithin(t *testing.T, conn net.Conn, d time.Duration) protocol.Packet {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(d))
	packet, err := protocol.ReadPacket(conn)
	if err != nil {
		t.Fatalf("expected a packet within %v: %v", d, err)
	}
	return packet
}

// expectNoPacket asserts the player receives nothing within the window.
func expectNoPacket(t *testing.T, conn net.Conn, d time.Duration) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(d))
	packet, err := protocol.ReadPacket(conn)
	if err == nil {
		t.Fatalf("expected no packet, got type=%d body=%q", packet.Type, packet.Body)
	}
	if !errors.Is(err, io.EOF) {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return // timeout = silence, as wanted
		}
	}
}

func TestRouteQueryFailureStaysSilent(t *testing.T) {
	var buf lockedBuffer
	dir := newTestDirectory(t, dummyBackends())
	router := &fakeRouter{err: errors.New("connection refused")}
	sess, peer := newRouteTestSession(t, dir, router, &buf)
	sess.maybeRouteToMatch(context.Background())
	expectNoPacket(t, peer, 300*time.Millisecond)
	if !strings.Contains(buf.String(), "assignment query failed") {
		t.Errorf("log must classify the failure as a query problem: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "staying in lobby") {
		t.Errorf("log must state the player stays in the lobby: %q", buf.String())
	}
}

func TestRouteNoAssignmentStaysSilent(t *testing.T) {
	var buf lockedBuffer
	dir := newTestDirectory(t, dummyBackends())
	router := &fakeRouter{decision: mcacascade.Decision{Outcome: mcacascade.OutcomeNoAssignment}}
	sess, peer := newRouteTestSession(t, dir, router, &buf)
	sess.maybeRouteToMatch(context.Background())
	expectNoPacket(t, peer, 300*time.Millisecond)
	if !strings.Contains(buf.String(), "no assignment") {
		t.Errorf("log missing no-assignment outcome: %q", buf.String())
	}
}

func TestRouteMessagesByOutcome(t *testing.T) {
	cases := []struct {
		name     string
		decision mcacascade.Decision
		wantText string
	}{
		{"no room", mcacascade.Decision{Outcome: mcacascade.OutcomeNoRoom, Reason: mcacascade.ReasonNoRoom},
			"no available room"},
		{"no join target", mcacascade.Decision{Outcome: mcacascade.OutcomeNoJoinTarget, GroupID: 7, MatchType: "1v1"},
			"no game server address"},
		{"unknown target", mcacascade.Decision{Outcome: mcacascade.OutcomeUnknownTarget, JoinTarget: "9.9.9.9:1"},
			"no configuration for the target"},
		// Directory probes never ran -> sub-a is unhealthy -> unready message.
		{"unhealthy target", mcacascade.Decision{Outcome: mcacascade.OutcomeRoute, BackendID: "sub-a"},
			"not ready"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf lockedBuffer
			dir := newTestDirectory(t, dummyBackends())
			router := &fakeRouter{decision: tc.decision}
			sess, peer := newRouteTestSession(t, dir, router, &buf)
			sess.maybeRouteToMatch(context.Background())
			packet := readPacketWithin(t, peer, 2*time.Second)
			if !strings.Contains(string(packet.Body), tc.wantText) {
				t.Fatalf("message %q does not contain %q", packet.Body, tc.wantText)
			}
		})
	}
}

func TestRouteAlreadyOnTargetIsNoOp(t *testing.T) {
	var buf lockedBuffer
	dir := newTestDirectory(t, dummyBackends())
	router := &fakeRouter{decision: mcacascade.Decision{Outcome: mcacascade.OutcomeRoute, BackendID: "lobby"}}
	sess, peer := newRouteTestSession(t, dir, router, &buf)
	sess.maybeRouteToMatch(context.Background())
	expectNoPacket(t, peer, 300*time.Millisecond)
	if !strings.Contains(buf.String(), "already on target backend") {
		t.Errorf("log missing already-on-target outcome: %q", buf.String())
	}
}

// TestRouteSendsReconnectToJumpPort is the happy path: a healthy mapped
// backend with a jump port produces a chat confirmation plus
// PACKET_RECONNECT_TO aimed at the proxy's own jump port.
func TestRouteSendsReconnectToJumpPort(t *testing.T) {
	subControl, subGame, _ := startJumpBackend(t, "sub-a", `[]`)
	lobbyControl, lobbyGame, _ := startJumpBackend(t, "lobby", `[]`)

	cfg := config.Config{
		DefaultBackendID: "lobby",
		PublicGameHost:   "cascade.test",
		Backends: []config.BackendConfig{
			{ID: "lobby", ControlURL: lobbyControl, GameAddress: lobbyGame, TrustMode: "open", JumpPort: 5298},
			{ID: "sub-a", ControlURL: subControl, GameAddress: subGame, TrustMode: "open", JumpPort: 5299},
		},
		HealthIntervalSec:       1,
		RoomSnapshotIntervalSec: 60,
	}
	dir, err := backend.NewDirectory(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir.Start(ctx)
	waitUntil(t, 5*time.Second, func() bool { return dir.BackendHealthy("sub-a") })

	var buf lockedBuffer
	router := &fakeRouter{decision: mcacascade.Decision{
		Outcome: mcacascade.OutcomeRoute, BackendID: "sub-a",
		GroupID: 42, MatchType: "1v1", TeamID: "A",
	}}
	lobbyClient, _ := dir.Client("lobby")
	clientPeer, clientProxy := net.Pipe()
	defer clientPeer.Close()
	sess := &session{
		server: &Server{
			cfg:       cfg,
			logger:    log.New(&buf, "", 0),
			directory: dir,
			mcaRouter: router,
		},
		client:        clientProxy,
		backendClient: lobbyClient,
		sessionID:     "session-mca-2",
		playerID:      "player-2",
		haUserUUID:    "ha-uuid-2",
		authenticated: true,
		locale:        "en",
		writeMu:       &sync.Mutex{},
	}

	sess.maybeRouteToMatch(ctx)

	sawConfirmation := false
	deadline := time.Now().Add(3 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("no reconnect packet within 3s")
		}
		packet := readPacketWithin(t, clientPeer, 3*time.Second)
		if packet.Type != protocol.TypeReconnectTo {
			if strings.Contains(string(packet.Body), "Match found") {
				sawConfirmation = true
			}
			continue
		}
		if !sawConfirmation {
			t.Fatal("reconnect sent without the player-facing confirmation message")
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
		if target != "cascade.test:5299" {
			t.Fatalf("reconnect target = %q, want cascade.test:5299 (proxy jump port, never the backend address)", target)
		}
		if !strings.Contains(buf.String(), "routing to backend sub-a") {
			t.Errorf("log missing routing decision: %q", buf.String())
		}
		return
	}
}
