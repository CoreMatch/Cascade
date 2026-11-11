package proxy

// 进程外插件接线测试：真 plugin.Hub + 契约形状的假插件进程（httptest）。
// 覆盖 player.route 否决拦截与放行后的观察副本/落地通知投递。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cascade/internal/backend"
	"cascade/internal/config"
	"cascade/internal/plugin"
	"cascade/internal/protocol"

	mcacascade "github.com/CoreMatch/MCA-Cascade"
)

const vetoTestSecretHex = "fedcba9876543210fedcba9876543210"

// vetoFakePlugin 是契约形状的假插件：manifest 协商 + 签名应答 + 可配置裁决
type vetoFakePlugin struct {
	secret []byte

	mu           sync.Mutex
	routeVerdict string // player.route 的应答正文
	counts       map[string]int
}

func newVetoFakePlugin(verdict string) *vetoFakePlugin {
	raw, _ := hex.DecodeString(vetoTestSecretHex)
	return &vetoFakePlugin{
		secret:       raw,
		routeVerdict: verdict,
		counts:       map[string]int{},
	}
}

func (f *vetoFakePlugin) signWrite(w http.ResponseWriter, status int, body []byte) {
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, f.secret)
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cascade-Timestamp", strconv.FormatInt(ts, 10))
	w.Header().Set("X-Cascade-Signature", "v1="+hex.EncodeToString(mac.Sum(nil)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func (f *vetoFakePlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/manifest":
		manifest := map[string]any{
			"schema":   "cascade-plugin/manifest",
			"protocol": "cascade-plugin/v1",
			"id":       "veto-fake",
			"version":  "0.0.1",
			"events": map[string]any{
				"player.registered": map[string]any{"versions": []int{1}},
				"player.route":      map[string]any{"versions": []int{1}, "veto": true},
				"player.routed":     map[string]any{"versions": []int{1}},
			},
		}
		body, _ := json.Marshal(manifest)
		f.signWrite(w, http.StatusOK, body)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/events/"):
		name := strings.TrimPrefix(r.URL.Path, "/v1/events/")
		_, _ = io.ReadAll(r.Body)
		f.mu.Lock()
		f.counts[name]++
		verdict := f.routeVerdict
		f.mu.Unlock()
		resp := []byte(`{}`)
		if name == "player.route" && verdict != "" {
			resp = []byte(verdict)
		}
		f.signWrite(w, http.StatusOK, resp)
	default:
		http.NotFound(w, r)
	}
}

func (f *vetoFakePlugin) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[name]
}

// startVetoTestEnv 起假插件 + 真 hub + 健康后端目录，返回装配好的会话
func startVetoTestEnv(t *testing.T, verdict string, logBuf *lockedBuffer) (*session, net.Conn, *vetoFakePlugin) {
	t.Helper()
	fp := newVetoFakePlugin(verdict)
	srv := httptest.NewServer(fp)
	t.Cleanup(srv.Close)

	hubCfg := plugin.NewConfig()
	hubCfg.Entries = []plugin.Entry{{
		Name:       "veto-fake",
		BaseURL:    srv.URL,
		Secrets:    []string{vetoTestSecretHex},
		VetoEvents: []string{plugin.EventPlayerRoute},
	}}
	hub, err := plugin.New(hubCfg, plugin.ProxyInfo{ProxyID: "test-proxy", HostVersion: plugin.HostVersion}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	waitUntil(t, 3*time.Second, func() bool {
		st := hub.Status()
		return len(st) == 1 && st[0].Ready
	})

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
	t.Cleanup(cancel)
	dir.Start(ctx)
	waitUntil(t, 5*time.Second, func() bool { return dir.BackendHealthy("sub-a") })

	lobbyClient, _ := dir.Client("lobby")
	clientPeer, clientProxy := net.Pipe()
	t.Cleanup(func() { _ = clientPeer.Close() })
	sess := &session{
		server: &Server{
			cfg:       cfg,
			logger:    log.New(logBuf, "", 0),
			directory: dir,
			plugins:   hub,
			mcaRouter: &fakeRouter{decision: mcacascade.Decision{
				Outcome: mcacascade.OutcomeRoute, BackendID: "sub-a",
				GroupID: 42, MatchType: "1v1", TeamID: "A", JoinTarget: "127.0.0.1:5202",
			}},
		},
		client:        clientProxy,
		backendClient: lobbyClient,
		sessionID:     "session-veto-1",
		playerID:      "player-veto",
		haUserUUID:    "ha-uuid-veto",
		authenticated: true,
		locale:        "en",
		writeMu:       &sync.Mutex{},
	}
	return sess, clientPeer, fp
}

func TestPluginVetoBlocksRoute(t *testing.T) {
	var buf lockedBuffer
	sess, peer, fp := startVetoTestEnv(t, `{"allowed":false,"reason":"维护窗口"}`, &buf)

	sess.maybeRouteToMatch(context.Background())

	packet := readPacketWithin(t, peer, 3*time.Second)
	if !strings.Contains(string(packet.Body), "Cannot enter the match") ||
		!strings.Contains(string(packet.Body), "维护窗口") {
		t.Fatalf("denial message missing reason: %q", packet.Body)
	}
	// 否决后不得有 178
	expectNoPacket(t, peer, 300*time.Millisecond)
	if !strings.Contains(buf.String(), "vetoed by plugin veto-fake") {
		t.Errorf("log missing veto record: %q", buf.String())
	}
	// 否决不产生观察副本，但否决咨询本身到达了插件
	waitUntil(t, 2*time.Second, func() bool { return fp.count("player.route") == 1 })
}

func TestPluginAllowLetsRouteProceedAndReceivesEvents(t *testing.T) {
	var buf lockedBuffer
	sess, peer, fp := startVetoTestEnv(t, `{"allowed":true}`, &buf)

	sess.maybeRouteToMatch(context.Background())

	// 178 照常到达
	deadline := time.Now().Add(3 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("no reconnect packet within 3s")
		}
		packet := readPacketWithin(t, peer, 3*time.Second)
		if packet.Type == protocol.TypeReconnectTo {
			break
		}
	}
	// 插件收到否决咨询 + 观察副本 + 落地通知
	waitUntil(t, 3*time.Second, func() bool {
		return fp.count("player.route") == 2 && fp.count("player.routed") == 1
	})
}
