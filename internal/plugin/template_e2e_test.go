package plugin

// 模板真进程 E2E：把 docs/plugins/template 编译成二进制，起真进程接真
// hub，验证协商/放行/否决/密钥错配四类路径（Avalon 侧同形测试为
// internal/plugin/template_e2e_test.go）。

import (
	"io"
	"log"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

const templateSecretHex = "a1b2c3d4e5f60718a1b2c3d4e5f60718"

// buildTemplatePlugin 编译模板插件，返回二进制路径
func buildTemplatePlugin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "route-watcher")
	cmd := exec.Command("go", "build", "-o", bin, "./docs/plugins/template")
	// 仓库根 = internal/plugin 的上两级
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build template plugin: %v\n%s", err, out)
	}
	return bin
}

// freePort 取一个空闲端口（模板进程监听用）
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// startTemplatePlugin 起真插件进程；返回基址与停止函数
func startTemplatePlugin(t *testing.T, extraArgs ...string) string {
	t.Helper()
	bin := buildTemplatePlugin(t)
	port := freePort(t)
	args := append([]string{
		"-addr", "127.0.0.1:" + itoa(port),
		"-secret", templateSecretHex,
	}, extraArgs...)
	cmd := exec.Command(bin, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start template plugin: %v", err)
	}
	base := "http://127.0.0.1:" + itoa(port)
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	// 等监听就绪
	waitUntil(t, 5*time.Second, func() bool {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+itoa(port), 200*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})
	return base
}

func itoa(v int) string { return strconv.Itoa(v) }

func newTemplateHub(t *testing.T, baseURL string, secretHex string, vetoEvents []string) *Hub {
	t.Helper()
	cfg := NewConfig()
	cfg.Entries = []Entry{{
		Name:       "route-watcher",
		BaseURL:    baseURL,
		Secrets:    []string{secretHex},
		VetoEvents: vetoEvents,
	}}
	hub, err := New(cfg, ProxyInfo{ProxyID: "template-test", HostVersion: HostVersion}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	return hub
}

func TestTemplatePluginRunsAgainstHub(t *testing.T) {
	base := startTemplatePlugin(t)
	hub := newTemplateHub(t, base, templateSecretHex, []string{EventPlayerRoute})
	waitUntil(t, 5*time.Second, func() bool {
		st := hub.Status()
		return len(st) == 1 && st[0].Ready && st[0].ManifestID == "route-watcher"
	})

	// 观察档：真进程收到 player.registered
	hub.EmitPlayerRegistered(PlayerInfo{PlayerID: "p1", Name: "Alice"}, "lobby")
	waitUntil(t, 5*time.Second, func() bool { return hub.Status()[0].Delivered >= 1 })

	// 否决档：模板默认放行；放行后观察副本与落地通知也应送达
	player, decision := admitTestPlayer()
	if d := hub.AdmitPlayerRoute(player, "lobby", decision); !d.Allowed {
		t.Fatalf("template plugin should allow by default: %+v", d)
	}
	hub.EmitPlayerRouted(player, decision.BackendID, "127.0.0.1:5199")
	waitUntil(t, 5*time.Second, func() bool { return hub.Status()[0].Delivered >= 3 })
	if hub.Status()[0].Dropped != 0 || hub.Status()[0].FailOpen != 0 {
		t.Errorf("unexpected losses: %+v", hub.Status()[0])
	}
}

func TestTemplatePluginDenyFlag(t *testing.T) {
	base := startTemplatePlugin(t, "-deny")
	hub := newTemplateHub(t, base, templateSecretHex, []string{EventPlayerRoute})
	waitUntil(t, 5*time.Second, func() bool { return hub.Status()[0].Ready })

	player, decision := admitTestPlayer()
	d := hub.AdmitPlayerRoute(player, "lobby", decision)
	if d.Allowed {
		t.Fatal("-deny template must veto")
	}
	if d.Plugin != "route-watcher" || d.Reason == "" {
		t.Errorf("decision = %+v", d)
	}
}

func TestTemplatePluginSecretMismatch(t *testing.T) {
	base := startTemplatePlugin(t) // 插件持 templateSecretHex
	// hub 用另一把密钥签名 → 插件验签拒绝 → 协商失败 → 永不就绪
	hub := newTemplateHub(t, base, "0000000000000000ffffffffffffffff", nil)
	time.Sleep(500 * time.Millisecond)
	if hub.Status()[0].Ready {
		t.Fatal("secret mismatch must not negotiate")
	}
	// 否决快速路径：未就绪不阻塞，直接放行（fail-open）
	player, decision := admitTestPlayer()
	if d := hub.AdmitPlayerRoute(player, "lobby", decision); !d.Allowed {
		t.Fatalf("fail-open violated: %+v", d)
	}
}
