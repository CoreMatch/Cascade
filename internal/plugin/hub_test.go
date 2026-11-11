package plugin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- 契约形状的假插件（测试用，等价于模板插件的最小内核） ---

type fakeVerdict struct {
	Status int    // HTTP 状态码；0 = 200
	Body   string // 应答正文；空 = {}
	NoSign bool   // true = 应答不签名（畸形应答演练）
	Hang   time.Duration
}

type fakePlugin struct {
	secret []byte

	mu        sync.Mutex
	events    map[string]int
	bodies    map[string][]string
	verdicts  map[string]fakeVerdict
	subscribe map[string]EventCapability
	manifest  fakeVerdict // 覆盖 manifest 应答（Status/NoSign/Hang）
}

func newFakePlugin(secretHex string) *fakePlugin {
	raw, _ := hex.DecodeString(secretHex)
	return &fakePlugin{
		secret:    raw,
		events:    map[string]int{},
		bodies:    map[string][]string{},
		verdicts:  map[string]fakeVerdict{},
		subscribe: map[string]EventCapability{},
	}
}

func (f *fakePlugin) sign(body []byte, w http.ResponseWriter) {
	ts := time.Now().Unix()
	mac := hmac.New(sha256.New, f.secret)
	fmt.Fprintf(mac, "%d.", ts)
	mac.Write(body)
	w.Header().Set(headerTimestamp, strconv.FormatInt(ts, 10))
	w.Header().Set(headerSignature, signaturePrefix+hex.EncodeToString(mac.Sum(nil)))
}

func (f *fakePlugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/manifest":
		f.serveManifest(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/events/"):
		f.serveEvent(w, r, strings.TrimPrefix(r.URL.Path, "/v1/events/"))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakePlugin) serveManifest(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	mv := f.manifest
	subscribe := map[string]EventCapability{}
	for k, v := range f.subscribe {
		subscribe[k] = v
	}
	f.mu.Unlock()
	if mv.Hang > 0 {
		time.Sleep(mv.Hang)
	}
	body, _ := json.Marshal(Manifest{
		Schema:   ManifestSchema,
		Protocol: ProtocolV1,
		ID:       "fake-plugin",
		Version:  "0.0.1",
		Events:   subscribe,
	})
	if mv.Status != 0 {
		http.Error(w, "forced", mv.Status)
		return
	}
	if !mv.NoSign {
		f.sign(body, w)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (f *fakePlugin) serveEvent(w http.ResponseWriter, r *http.Request, name string) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	v := f.verdicts[name]
	f.events[name]++
	f.bodies[name] = append(f.bodies[name], string(body))
	f.mu.Unlock()
	if v.Hang > 0 {
		time.Sleep(v.Hang)
	}
	resp := []byte(v.Body)
	if len(resp) == 0 {
		resp = []byte(`{}`)
	}
	status := v.Status
	if status == 0 {
		status = http.StatusOK
	}
	if !v.NoSign {
		f.sign(resp, w)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(resp)
}

func (f *fakePlugin) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.events[name]
}

func (f *fakePlugin) lastBody(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies[name]) == 0 {
		return ""
	}
	return f.bodies[name][len(f.bodies[name])-1]
}

const testSecretHex = "0123456789abcdef0123456789abcdef"

// startHubWith 起一个带假插件的 hub；返回 hub、插件与清理函数
func startHubWith(t *testing.T, cfgMutate func(*Config), fp *fakePlugin) *Hub {
	t.Helper()
	srv := httptest.NewServer(fp)
	t.Cleanup(srv.Close)

	cfg := NewConfig()
	cfg.Entries = []Entry{{
		Name:    "fake",
		BaseURL: srv.URL,
		Secrets: []string{testSecretHex},
	}}
	if cfgMutate != nil {
		cfgMutate(&cfg)
	}
	hub, err := New(cfg, ProxyInfo{ProxyID: "test-proxy", HostVersion: HostVersion}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	return hub
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// --- 协商与观察档投递 ---

func TestNegotiateAndObserveDelivery(t *testing.T) {
	fp := newFakePlugin(testSecretHex)
	fp.subscribe[EventPlayerRegistered] = EventCapability{Versions: []int{1}}
	hub := startHubWith(t, nil, fp)

	waitUntil(t, 3*time.Second, func() bool {
		st := hub.Status()
		return len(st) == 1 && st[0].Ready && st[0].ManifestID == "fake-plugin"
	})

	hub.EmitPlayerRegistered(PlayerInfo{PlayerID: "p1", Name: "Alice", Authenticated: true, HAUserUUID: "u1"}, "lobby")
	waitUntil(t, 3*time.Second, func() bool { return fp.count(EventPlayerRegistered) == 1 })

	var env struct {
		EventID      string `json:"eventId"`
		Event        string `json:"event"`
		EventVersion int    `json:"eventVersion"`
		OccurredAt   string `json:"occurredAt"`
		Proxy        struct {
			ProxyID     string `json:"proxyId"`
			HostVersion string `json:"hostVersion"`
		} `json:"proxy"`
		Player struct {
			PlayerID      string `json:"playerId"`
			Name          string `json:"name"`
			Authenticated bool   `json:"authenticated"`
			HAUserUUID    string `json:"haUserUuid"`
		} `json:"player"`
		Backend struct {
			BackendID string `json:"backendId"`
		} `json:"backend"`
	}
	body := fp.lastBody(EventPlayerRegistered)
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("payload not JSON: %v\n%s", err, body)
	}
	if !strings.HasPrefix(env.EventID, "evt_") || env.Event != EventPlayerRegistered || env.EventVersion != 1 {
		t.Errorf("envelope identity wrong: %+v", env)
	}
	if env.Proxy.ProxyID != "test-proxy" || env.Player.PlayerID != "p1" || env.Player.HAUserUUID != "u1" || env.Backend.BackendID != "lobby" {
		t.Errorf("payload fields wrong: %s", body)
	}

	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Delivered == 1 })
}

func TestUnsubscribedEventNotDelivered(t *testing.T) {
	fp := newFakePlugin(testSecretHex)
	fp.subscribe[EventPlayerRouted] = EventCapability{Versions: []int{1}} // 只订阅 routed
	hub := startHubWith(t, nil, fp)
	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Ready })

	hub.EmitPlayerRegistered(PlayerInfo{PlayerID: "p1"}, "lobby")
	time.Sleep(200 * time.Millisecond)
	if fp.count(EventPlayerRegistered) != 0 {
		t.Fatal("unsubscribed event was delivered")
	}
}

func TestObserveFailureDropsWithoutRetry(t *testing.T) {
	fp := newFakePlugin(testSecretHex)
	fp.subscribe[EventPlayerRegistered] = EventCapability{Versions: []int{1}}
	fp.verdicts[EventPlayerRegistered] = fakeVerdict{Status: http.StatusInternalServerError}
	hub := startHubWith(t, nil, fp)
	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Ready })

	hub.EmitPlayerRegistered(PlayerInfo{PlayerID: "p1"}, "lobby")
	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Dropped == 1 })
	if fp.count(EventPlayerRegistered) != 1 {
		t.Fatalf("attempts = %d, want exactly 1 (observe 不重试)", fp.count(EventPlayerRegistered))
	}
}

func TestDisabledPluginGetsNothing(t *testing.T) {
	fp := newFakePlugin(testSecretHex)
	fp.subscribe[EventPlayerRegistered] = EventCapability{Versions: []int{1}}
	hub := startHubWith(t, func(c *Config) {
		disabled := false
		c.Entries[0].Enabled = &disabled
	}, fp)

	hub.EmitPlayerRegistered(PlayerInfo{PlayerID: "p1"}, "lobby")
	time.Sleep(200 * time.Millisecond)
	if fp.count(EventPlayerRegistered) != 0 {
		t.Fatal("disabled plugin received an event")
	}
	st := hub.Status()
	if st[0].Enabled || st[0].Ready {
		t.Errorf("disabled plugin status = %+v", st[0])
	}
}

// --- 否决档 ---

func admitTestPlayer() (PlayerInfo, RouteDecision) {
	return PlayerInfo{PlayerID: "p1", Name: "Alice", Authenticated: true, HAUserUUID: "u1"},
		RouteDecision{Source: "mca", BackendID: "sub-a", JoinTarget: "127.0.0.1:5202", GroupID: 42, MatchType: "1v1", TeamID: "A"}
}

func TestVetoDenyWithReason(t *testing.T) {
	fp := newFakePlugin(testSecretHex)
	fp.subscribe[EventPlayerRoute] = EventCapability{Versions: []int{1}, Veto: true}
	fp.verdicts[EventPlayerRoute] = fakeVerdict{
		Body: `{"allowed":false,"reason":"维护中"}`,
	}
	hub := startHubWith(t, func(c *Config) {
		c.Entries[0].VetoEvents = []string{EventPlayerRoute}
	}, fp)
	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Ready })

	player, decision := admitTestPlayer()
	d := hub.AdmitPlayerRoute(player, "lobby", decision)
	if d.Allowed {
		t.Fatal("expected denial")
	}
	if !strings.Contains(d.Reason, "维护中") || !strings.Contains(d.Reason, "fake") {
		t.Errorf("reason = %q, want plugin-prefixed readable reason", d.Reason)
	}
	if d.Plugin != "fake" {
		t.Errorf("Plugin = %q", d.Plugin)
	}
	if hub.Status()[0].Denied != 1 {
		t.Errorf("denied counter = %d", hub.Status()[0].Denied)
	}
}

func TestVetoAllowAndObserveCopy(t *testing.T) {
	fp := newFakePlugin(testSecretHex)
	fp.subscribe[EventPlayerRoute] = EventCapability{Versions: []int{1}, Veto: true}
	fp.verdicts[EventPlayerRoute] = fakeVerdict{Body: `{"allowed":true}`}
	hub := startHubWith(t, func(c *Config) {
		c.Entries[0].VetoEvents = []string{EventPlayerRoute}
	}, fp)
	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Ready })

	player, decision := admitTestPlayer()
	if d := hub.AdmitPlayerRoute(player, "lobby", decision); !d.Allowed {
		t.Fatalf("expected allow, got %+v", d)
	}
	// 放行后观察副本到达（同一事件端点，计数含否决咨询那次 = 2）
	waitUntil(t, 3*time.Second, func() bool { return fp.count(EventPlayerRoute) == 2 })
}

func TestVetoFailOpenMatrix(t *testing.T) {
	cases := []struct {
		name    string
		verdict fakeVerdict
	}{
		{"http 500", fakeVerdict{Status: http.StatusInternalServerError}},
		{"deny without reason", fakeVerdict{Body: `{"allowed":false}`}},
		{"malformed verdict", fakeVerdict{Body: `{`}},
		{"missing allowed", fakeVerdict{Body: `{"reason":"x"}`}},
		{"unsigned response", fakeVerdict{Body: `{"allowed":false,"reason":"x"}`, NoSign: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := newFakePlugin(testSecretHex)
			fp.subscribe[EventPlayerRoute] = EventCapability{Versions: []int{1}, Veto: true}
			fp.verdicts[EventPlayerRoute] = tc.verdict
			hub := startHubWith(t, func(c *Config) {
				c.Entries[0].VetoEvents = []string{EventPlayerRoute}
			}, fp)
			waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Ready })

			player, decision := admitTestPlayer()
			d := hub.AdmitPlayerRoute(player, "lobby", decision)
			if !d.Allowed {
				t.Fatalf("fail-open violated: %+v", d)
			}
			if hub.Status()[0].FailOpen != 1 {
				t.Errorf("failOpen counter = %d, want 1", hub.Status()[0].FailOpen)
			}
		})
	}
}

func TestVetoTimeoutFailsOpen(t *testing.T) {
	fp := newFakePlugin(testSecretHex)
	fp.subscribe[EventPlayerRoute] = EventCapability{Versions: []int{1}, Veto: true}
	fp.verdicts[EventPlayerRoute] = fakeVerdict{Hang: 800 * time.Millisecond, Body: `{"allowed":false,"reason":"too late"}`}
	hub := startHubWith(t, func(c *Config) {
		c.AdmissionTimeoutMs = 100
		c.Entries[0].VetoEvents = []string{EventPlayerRoute}
	}, fp)
	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Ready })

	player, decision := admitTestPlayer()
	start := time.Now()
	d := hub.AdmitPlayerRoute(player, "lobby", decision)
	if !d.Allowed {
		t.Fatalf("timeout must fail open: %+v", d)
	}
	if elapsed := time.Since(start); elapsed > 700*time.Millisecond {
		t.Errorf("veto call not bounded by admission timeout: %v", elapsed)
	}
	if hub.Status()[0].FailOpen != 1 {
		t.Errorf("failOpen counter = %d", hub.Status()[0].FailOpen)
	}
}

func TestVetoWithoutGrantIsObserveOnly(t *testing.T) {
	fp := newFakePlugin(testSecretHex)
	// manifest 声明了 veto，但运营未授权 → 不参与否决咨询
	fp.subscribe[EventPlayerRoute] = EventCapability{Versions: []int{1}, Veto: true}
	fp.verdicts[EventPlayerRoute] = fakeVerdict{Body: `{"allowed":false,"reason":"should not be consulted"}`}
	hub := startHubWith(t, nil, fp) // 无 vetoEvents 授权
	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Ready })

	player, decision := admitTestPlayer()
	d := hub.AdmitPlayerRoute(player, "lobby", decision)
	if !d.Allowed {
		t.Fatalf("ungranted plugin must not veto: %+v", d)
	}
	// 观察副本仍然投递
	waitUntil(t, 3*time.Second, func() bool { return fp.count(EventPlayerRoute) == 1 })
}

func TestVetoNotReadyPluginFailsOpen(t *testing.T) {
	// 授权插件不可达：启动协商失败 → not ready → admit 计 fail-open 放行
	cfg := NewConfig()
	cfg.AdmissionTimeoutMs = 150
	cfg.Entries = []Entry{{
		Name:       "dead",
		BaseURL:    "http://127.0.0.1:1", // 拒连
		Secrets:    []string{testSecretHex},
		VetoEvents: []string{EventPlayerRoute},
	}}
	hub, err := New(cfg, ProxyInfo{ProxyID: "t"}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	player, decision := admitTestPlayer()
	d := hub.AdmitPlayerRoute(player, "lobby", decision)
	if !d.Allowed {
		t.Fatalf("not-ready veto plugin must fail open: %+v", d)
	}
	if hub.Status()[0].FailOpen != 1 {
		t.Errorf("failOpen = %d, want 1", hub.Status()[0].FailOpen)
	}
}

// --- 队列 ---

func TestQueueDropOldest(t *testing.T) {
	q := newQueue(2)
	for i := 0; i < 4; i++ {
		dropped := q.append(outboxRecord{EventID: fmt.Sprintf("e%d", i)})
		if i < 2 && dropped != 0 {
			t.Fatalf("append %d dropped %d prematurely", i, dropped)
		}
		if i >= 2 && dropped != 1 {
			t.Fatalf("append %d dropped %d, want 1", i, dropped)
		}
	}
	rec, ok := q.peek()
	if !ok || rec.EventID != "e2" {
		t.Fatalf("peek = %+v, want e2 (oldest dropped)", rec)
	}
}

// --- 签名 ---

func TestSignatureRoundtripAndRejection(t *testing.T) {
	secret := []byte("0123456789abcdef")
	body := []byte(`{"a":1}`)
	ts := time.Now().Unix()
	sig := signBody(secret, ts, body)
	if !strings.HasPrefix(sig, signaturePrefix) {
		t.Fatalf("signature %q missing prefix", sig)
	}

	ok := http.Response{Header: http.Header{}}
	ok.Header.Set(headerTimestamp, strconv.FormatInt(ts, 10))
	ok.Header.Set(headerSignature, sig)
	if err := verifyResponse([][]byte{secret}, &ok, body); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}

	// 正文篡改
	if err := verifyResponse([][]byte{secret}, &ok, []byte(`{"a":2}`)); err == nil {
		t.Fatal("tampered body accepted")
	}
	// 时间戳过期
	old := http.Response{Header: http.Header{}}
	old.Header.Set(headerTimestamp, strconv.FormatInt(ts-400, 10))
	old.Header.Set(headerSignature, signBody(secret, ts-400, body))
	if err := verifyResponse([][]byte{secret}, &old, body); err == nil {
		t.Fatal("stale timestamp accepted")
	}
	// 缺头
	missing := http.Response{Header: http.Header{}}
	if err := verifyResponse([][]byte{secret}, &missing, body); err == nil {
		t.Fatal("missing headers accepted")
	}
	// 密钥轮换：旧密钥签名仍被新列表接受
	rotated := [][]byte{[]byte("ffffffffffffffff"), secret}
	if err := verifyResponse(rotated, &ok, body); err != nil {
		t.Fatalf("rotation overlap rejected: %v", err)
	}
}

// --- 配置 ---

func TestLoadConfigFileCreatesDefault(t *testing.T) {
	dir := t.TempDir()
	oldWd := chdir(t, dir)
	defer chdir(t, oldWd)

	cfg, path, err := LoadConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Entries) != 0 || cfg.Version != 1 {
		t.Fatalf("default config = %+v", cfg)
	}
	if path != "Plugins.json" {
		t.Errorf("path = %q", path)
	}
	// 第二次读取：文件已存在
	cfg2, _, err := LoadConfigFile()
	if err != nil || len(cfg2.Entries) != 0 {
		t.Fatalf("reload failed: %+v %v", cfg2, err)
	}
}

func TestLoadConfigFileRejectsInvalid(t *testing.T) {
	dir := t.TempDir()
	oldWd := chdir(t, dir)
	defer chdir(t, oldWd)

	cases := []struct {
		name, content, want string
	}{
		{"bad json", `{`, "parse"},
		{"wrong version", `{"version":2}`, "version"},
		{"empty name", `{"version":1,"entries":[{"name":"","baseUrl":"http://127.0.0.1:1","secrets":["0123456789abcdef0123456789abcdef"]}]}`, "empty name"},
		{"duplicate name", `{"version":1,"entries":[
			{"name":"a","baseUrl":"http://127.0.0.1:1","secrets":["0123456789abcdef0123456789abcdef"]},
			{"name":"a","baseUrl":"http://127.0.0.1:2","secrets":["0123456789abcdef0123456789abcdef"]}]}`, "duplicate"},
		{"bad base url", `{"version":1,"entries":[{"name":"a","baseUrl":"ftp://x","secrets":["0123456789abcdef0123456789abcdef"]}]}`, "baseUrl"},
		{"no secrets", `{"version":1,"entries":[{"name":"a","baseUrl":"http://127.0.0.1:1","secrets":[]}]}`, "secret"},
		{"short secret", `{"version":1,"entries":[{"name":"a","baseUrl":"http://127.0.0.1:1","secrets":["abcd"]}]}`, "secret"},
		{"non-hex secret", `{"version":1,"entries":[{"name":"a","baseUrl":"http://127.0.0.1:1","secrets":["zz23456789abcdef0123456789abcdef"]}]}`, "secret"},
		{"unknown veto event", `{"version":1,"entries":[{"name":"a","baseUrl":"http://127.0.0.1:1","secrets":["0123456789abcdef0123456789abcdef"],"vetoEvents":["game.over"]}]}`, "vetoEvents"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := writeFile(t, "Plugins.json", tc.content); err != nil {
				t.Fatal(err)
			}
			_, _, err := LoadConfigFile()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestManifestValidation(t *testing.T) {
	bad := &Manifest{Schema: "wrong", Protocol: ProtocolV1, ID: "x", Version: "1"}
	if err := bad.validate(); err == nil {
		t.Fatal("wrong schema accepted")
	}
	bad2 := &Manifest{Schema: ManifestSchema, Protocol: "avalon-plugin/v1", ID: "x", Version: "1"}
	if err := bad2.validate(); err == nil {
		t.Fatal("foreign protocol accepted (Cascade plugins must not speak the Avalon dialect)")
	}
	okm := &Manifest{Schema: ManifestSchema, Protocol: ProtocolV1, ID: "x", Version: "1",
		Events: map[string]EventCapability{EventPlayerRoute: {Versions: []int{1}}}}
	if okm.supports(EventPlayerRoute) != 1 || okm.supports(EventPlayerRouted) != 0 {
		t.Fatal("version negotiation wrong")
	}
}

func TestSanitizeReason(t *testing.T) {
	if got := sanitizeReason("  维护\x00中 "); got != "维护中" {
		t.Errorf("sanitize = %q", got)
	}
	long := strings.Repeat("长", maxReasonRunes+50)
	if got := sanitizeReason(long); len([]rune(got)) != maxReasonRunes {
		t.Errorf("truncate failed: %d runes", len([]rune(got)))
	}
}

// --- 测试工具 ---

func chdir(t *testing.T, dir string) string {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return old
}

func writeFile(t *testing.T, name, content string) error {
	t.Helper()
	return os.WriteFile(name, []byte(content), 0644)
}
