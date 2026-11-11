package plugin

// manifest 协商边界与失败隔离/超时行为测试（knowledge/0008 声明的语义
// 逐条机器化）：协商失败形状、版本交集、64KB 上限、自报超时封顶、
// 观察档超大应答/重定向/超时、队列满丢最旧、未就绪积压补投、连接断开
// 后自动重协商。

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// --- manifest 协商边界 ---

func TestManifestNegotiationBoundaries(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*fakePlugin)
	}{
		{"wrong schema", func(f *fakePlugin) { f.mSchema = "avalon-plugin/manifest" }},
		{"wrong protocol", func(f *fakePlugin) { f.mProtocol = "avalon-plugin/v1" }},
		{"empty id", func(f *fakePlugin) { f.mID = " " }},
		{"empty version", func(f *fakePlugin) { f.mVersion = " " }},
		{"manifest http 500", func(f *fakePlugin) { f.manifest = fakeVerdict{Status: http.StatusInternalServerError} }},
		{"manifest unsigned", func(f *fakePlugin) { f.manifest = fakeVerdict{NoSign: true} }},
		{"manifest oversized", func(f *fakePlugin) { f.padManifestKB = 70 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fp := newFakePlugin(testSecretHex)
			fp.subscribe[EventPlayerRegistered] = EventCapability{Versions: []int{1}}
			tc.mutate(fp)
			hub := startHubWith(t, nil, fp)

			// 协商失败：不得就绪，也不得投递
			time.Sleep(300 * time.Millisecond)
			if hub.Status()[0].Ready {
				t.Fatal("invalid manifest must not negotiate to ready")
			}
			hub.EmitPlayerRegistered(PlayerInfo{PlayerID: "p1"}, "lobby")
			time.Sleep(200 * time.Millisecond)
			if fp.count(EventPlayerRegistered) != 0 {
				t.Fatal("event delivered despite failed negotiation")
			}
			// 未就绪的授权否决（若有）也必须 fail-open —— 这里验证不阻塞
			player, decision := admitTestPlayer()
			if d := hub.AdmitPlayerRoute(player, "lobby", decision); !d.Allowed {
				t.Fatalf("fail-open violated: %+v", d)
			}
		})
	}
}

func TestVersionIntersectionExcludesUnsupported(t *testing.T) {
	fp := newFakePlugin(testSecretHex)
	// 插件只支持版本 2 —— Cascade v1 只发版本 1，交集为空
	fp.subscribe[EventPlayerRegistered] = EventCapability{Versions: []int{2}}
	hub := startHubWith(t, nil, fp)
	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Ready })

	if got := hub.Status()[0].Events; len(got) != 0 {
		t.Fatalf("negotiated events = %v, want none", got)
	}
	hub.EmitPlayerRegistered(PlayerInfo{PlayerID: "p1"}, "lobby")
	time.Sleep(200 * time.Millisecond)
	if fp.count(EventPlayerRegistered) != 0 {
		t.Fatal("event with no version intersection must not be delivered")
	}
	if hub.Status()[0].Delivered != 0 {
		t.Errorf("delivered = %d, want 0", hub.Status()[0].Delivered)
	}
}

// --- 自报超时封顶 ---

func TestManifestTimeoutCapsAdmission(t *testing.T) {
	fp := newFakePlugin(testSecretHex)
	fp.subscribe[EventPlayerRoute] = EventCapability{Versions: []int{1}, Veto: true}
	fp.verdicts[EventPlayerRoute] = fakeVerdict{Hang: 400 * time.Millisecond, Body: `{"allowed":false,"reason":"late"}`}
	hub := startHubWith(t, func(c *Config) {
		c.AdmissionTimeoutMs = 2000 // 宿主上限 2s
		c.Entries[0].VetoEvents = []string{EventPlayerRoute}
	}, fp)
	// 用 manifest 覆盖无法直接设 timeoutMs —— 通过 ready 后替换 manifest 不行，
	// 这里直接验证 cappedTimeout 的取值逻辑。
	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Ready })

	p := hub.plugins[0]
	p.mu.Lock()
	p.manifest.TimeoutMs = 80 // 插件自报 80ms 预算
	p.mu.Unlock()

	player, decision := admitTestPlayer()
	start := time.Now()
	d := hub.AdmitPlayerRoute(player, "lobby", decision)
	elapsed := time.Since(start)
	if !d.Allowed {
		t.Fatalf("timeout must fail open: %+v", d)
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("veto not capped by plugin-declared timeoutMs: %v", elapsed)
	}
	if hub.Status()[0].FailOpen != 1 {
		t.Errorf("failOpen = %d, want 1", hub.Status()[0].FailOpen)
	}
}

// --- 观察档失败隔离 ---

func TestObserveOversizedResponseDropped(t *testing.T) {
	fp := newFakePlugin(testSecretHex)
	fp.subscribe[EventPlayerRegistered] = EventCapability{Versions: []int{1}}
	fp.verdicts[EventPlayerRegistered] = fakeVerdict{Body: strings.Repeat("x", 70*1024)}
	hub := startHubWith(t, nil, fp)
	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Ready })

	hub.EmitPlayerRegistered(PlayerInfo{PlayerID: "p1"}, "lobby")
	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Dropped == 1 })
	if hub.Status()[0].Delivered != 0 {
		t.Error("oversized ack must not count as delivered")
	}
}

func TestObserveRedirectNotFollowed(t *testing.T) {
	// 3xx 不跟随：重定向后的请求不再携带原签名语义，一律按失败处理
	fp := newFakePlugin(testSecretHex)
	fp.subscribe[EventPlayerRegistered] = EventCapability{Versions: []int{1}}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)

	// 直接用重定向服务器做插件基址
	cfg := NewConfig()
	cfg.Entries = []Entry{{Name: "redir", BaseURL: redirect.URL, Secrets: []string{testSecretHex}}}
	hub, err := New(cfg, ProxyInfo{ProxyID: "t"}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	// manifest 也是 302 → 永不就绪；观察事件入队后在就绪前积压，
	// 这里验证否决快速路径不受影响。
	time.Sleep(200 * time.Millisecond)
	if hub.Status()[0].Ready {
		t.Fatal("redirecting manifest must not negotiate")
	}
	player, decision := admitTestPlayer()
	if d := hub.AdmitPlayerRoute(player, "lobby", decision); !d.Allowed {
		t.Fatalf("fail-open violated: %+v", d)
	}
}

func TestObserveDeliveryTimeoutDropped(t *testing.T) {
	fp := newFakePlugin(testSecretHex)
	fp.subscribe[EventPlayerRegistered] = EventCapability{Versions: []int{1}}
	fp.verdicts[EventPlayerRegistered] = fakeVerdict{Hang: 500 * time.Millisecond}
	hub := startHubWith(t, func(c *Config) { c.DeliveryTimeoutMs = 100 }, fp)
	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Ready })

	hub.EmitPlayerRegistered(PlayerInfo{PlayerID: "p1"}, "lobby")
	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Dropped == 1 })
	if fp.count(EventPlayerRegistered) != 1 {
		t.Fatalf("attempts = %d, want exactly 1 (observe 不重试)", fp.count(EventPlayerRegistered))
	}
}

func TestQueueFullDropsOldestAtHubLevel(t *testing.T) {
	// 插件不可达（永不就绪）：入队走积压路径，容量 1 时丢最旧
	cfg := NewConfig()
	cfg.OutboxCapacity = 1
	cfg.AdmissionTimeoutMs = 100
	cfg.Entries = []Entry{{Name: "down", BaseURL: "http://127.0.0.1:1", Secrets: []string{testSecretHex}}}
	hub, err := New(cfg, ProxyInfo{ProxyID: "t"}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()

	for i := 0; i < 3; i++ {
		hub.EmitPlayerRegistered(PlayerInfo{PlayerID: "p"}, "lobby")
	}
	waitUntil(t, 2*time.Second, func() bool { return hub.Status()[0].Dropped == 2 })
	if hub.Status()[0].Queued != 1 {
		t.Errorf("queued = %d, want 1 (newest kept)", hub.Status()[0].Queued)
	}
}

// --- 未就绪积压补投与断线重协商 ---

// withFastBackoff 把 manifest 重协商退避换成毫秒级（测试专用，结束恢复）
func withFastBackoff(t *testing.T) {
	t.Helper()
	oldInit, oldMax := initialManifestBackoff, maxManifestBackoff
	initialManifestBackoff, maxManifestBackoff = 20*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() {
		initialManifestBackoff, maxManifestBackoff = oldInit, oldMax
	})
}

// freeAddr 取一个空闲的 127.0.0.1 地址并立即释放（用于"先不可达、后上线"
// 的场景）
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return "http://" + addr
}

// serveAt 在指定基址上起服务，返回停止函数
func serveAt(t *testing.T, base string, handler http.Handler) func() {
	t.Helper()
	ln, err := net.Listen("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatalf("listen %s: %v", base, err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return func() { _ = srv.Close() }
}

func TestBacklogDeliveredAfterNegotiation(t *testing.T) {
	withFastBackoff(t)
	fp := newFakePlugin(testSecretHex)
	fp.subscribe[EventPlayerRegistered] = EventCapability{Versions: []int{1}}
	addr := freeAddr(t) // 启动时不可达

	cfg := NewConfig()
	cfg.AdmissionTimeoutMs = 150
	cfg.Entries = []Entry{{Name: "late", BaseURL: addr, Secrets: []string{testSecretHex}}}
	hub, err := New(cfg, ProxyInfo{ProxyID: "t"}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	if hub.Status()[0].Ready {
		t.Fatal("plugin must not be ready while unreachable")
	}

	// 未就绪期间入队（积压）
	hub.EmitPlayerRegistered(PlayerInfo{PlayerID: "p1"}, "lobby")

	// 插件上线 → 后台重协商成功 → 积压补投
	serveAt(t, addr, fp)
	waitUntil(t, 5*time.Second, func() bool {
		st := hub.Status()[0]
		return st.Ready && st.Delivered == 1
	})
	if fp.count(EventPlayerRegistered) != 1 {
		t.Fatalf("backlog deliveries = %d, want 1", fp.count(EventPlayerRegistered))
	}
}

func TestRenegotiateAfterConnectionLoss(t *testing.T) {
	withFastBackoff(t)
	fp := newFakePlugin(testSecretHex)
	fp.subscribe[EventPlayerRegistered] = EventCapability{Versions: []int{1}}
	addr := freeAddr(t)
	stop := serveAt(t, addr, fp)

	cfg := NewConfig()
	cfg.AdmissionTimeoutMs = 200
	cfg.Entries = []Entry{{Name: "flaky", BaseURL: addr, Secrets: []string{testSecretHex}}}
	hub, err := New(cfg, ProxyInfo{ProxyID: "t"}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	waitUntil(t, 3*time.Second, func() bool { return hub.Status()[0].Ready })

	// 插件进程消失：下一次投递触发连接级失败 → unready → 重协商循环
	stop()
	hub.EmitPlayerRegistered(PlayerInfo{PlayerID: "p1"}, "lobby")
	waitUntil(t, 3*time.Second, func() bool { return !hub.Status()[0].Ready })

	// 插件复活：自动重新协商并就绪（主流程无需任何干预）
	serveAt(t, addr, fp)
	waitUntil(t, 5*time.Second, func() bool { return hub.Status()[0].Ready })
}

// --- 事件 ID 形状 ---

func TestEventIDShapeAndUniqueness(t *testing.T) {
	now := time.Now()
	seen := map[string]bool{}
	for i := 0; i < 256; i++ {
		id := newEventID(now)
		if !strings.HasPrefix(id, "evt_") || len(id) != 4+26 {
			t.Fatalf("bad event id %q", id)
		}
		if seen[id] {
			t.Fatalf("duplicate event id %q", id)
		}
		seen[id] = true
	}
}
