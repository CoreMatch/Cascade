# Appendix — Plaintext Transport End-to-End Test (for the RW-HPS Go Server)

- **Status**: appendix (not an ADR; referenced by 0006)
- **Date**: 2026-11-05

## What this is

The plaintext counterpart of the RW-HPS Go Server's
`TestCascadeRealProxyE2E` (which exercises the mTLS path). It proves
contract v1.2 "Proxy transport selection" end to end: a gateway with **no
server certificates** (plaintext transport, trust `open`) plus a Cascade
configured with `trust_mode: open` / `tls_mode: auto` resolves to
`transport=plain`, needs no certificate material, and still carries the
full chain — room-code routing, CASC preamble, identity binding, engine
forwarding, and a real game to settlement.

The test belongs to the **RW-HPS repository** (`Go-Server`, package
`gateway_test`); it is parked here because that repository is maintained
under a separate task stream. When it lands there, this appendix can be
replaced by a link.

## Result (2026-11-05)

`TestCascadePlainTransportE2E` PASS in 13.4 s against this repository at
commit `cfa374b` (transport selection) with the RW-HPS Go Server at
`ed92ff0f` and its prebuilt `Helper.jar`:

- Go gateway logged `control API listening on 127.0.0.1:<port> (明文)`.
- Cascade logged `[backend] rwhps-plain: transport=plain (trust_mode "open", tls_mode "auto")`.
- Fake client received the engine's Preregister answer (161, 115 bytes).
- Session bound to player UUID `8899aabbccddeeff`.
- Real game (Crossing Large 10p, 2 AI) produced a settlement in 6 s.
- The fake client's KICK(150) "core units … different" is the engine's
  unit-checksum anti-cheat against synthetic clients (DECISIONS D-16),
  expected and harmless.

## Reproduction layout

`go test` resolves the repository root as four levels above
`Go-Server/internal/net/gateway`, so the copy must mirror the RW-HPS
layout:

```
<root>/
  Go-Server/          # copy of RW-HPS/Go-Server (module rwhps)
    internal/net/gateway/cascade_plain_e2e_test.go   # file below
  Helper -> <RW-HPS>/Helper      # symlink (provides build/libs/Helper.jar)
  data   -> <RW-HPS>/data        # symlink (provides data/gameData)
```

Run:

```bash
cd <root>/Go-Server
RWHPS_CASCADE_SRC=<Cascade checkout> \
  go test ./internal/net/gateway/ -run TestCascadePlainTransportE2E -v -count=1
```

Skips itself when `Helper.jar`, JDK 17, the Cascade source, or the Go
toolchain is unavailable (same contract as `TestCascadeRealProxyE2E`).

## Test source (`Go-Server/internal/net/gateway/cascade_plain_e2e_test.go`)

```go
package gateway_test

// 明文传输联调 (契约 v1.2 "Proxy transport selection"): Go 网关不配服务端
// 证书 (传输=明文, trust=open), Cascade 声明 trust_mode=open / tls_mode=auto
// -> 推导出明文传输, 不携带任何 mTLS 材料. 验证:
//   客户端连 Cascade -> 明文前导/门控 -> 身份绑定 -> 转发进引擎 -> 真实开局到结算.
//
// 与 TestCascadeRealProxyE2E (mTLS 路径) 互补; 前置不满足时同样跳过.

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"rwhps/internal/game"
	"rwhps/internal/game/external"
	"rwhps/internal/game/room"
	"rwhps/internal/net/core"
	"rwhps/internal/net/gateway"
	gameserver "rwhps/internal/net/gameserver"
	rwnet "rwhps/internal/net"
	"rwhps/internal/util/file"
	"rwhps/internal/util/file/plugindata"
)

// startPlainGoBackend 与 startGoBackend 相同, 但不配任何 TLS 材料:
// 游戏口与控制面均为明文, 信任档 open (DECISIONS D-24 默认).
func startPlainGoBackend(t *testing.T, java, jar string, gamePort, controlPort int) (*external.Backend, *gateway.Gateway, func()) {
	t.Helper()
	root := filepath.Join("..", "..", "..", "..")

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	gameData, err := filepath.Abs(filepath.Join(root, "data", "gameData"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gameData, filepath.Join(dataDir, "data", "gameData")); err != nil {
		t.Skipf("软链游戏数据失败: %v", err)
	}

	backend, err := external.New(external.Config{
		JavaPath:     java,
		HelperPath:   jar,
		ExtraArgs:    []string{"-Drwhps.helper.dataPath=" + dataDir, "-Xmx512m"},
		HelloTimeout: 90 * time.Second,
		OpTimeout:    120 * time.Second,
	})
	if err != nil {
		t.Fatalf("external.New: %v", err)
	}
	if err := backend.EnsureRunning(); err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}

	gameserver.AttachBackend(backend)
	room.SetDefault(room.NewServerRoom(game.NewGameSession("GW01", backend)))

	if _, err := backend.Request(&external.Request{Op: external.OpRoomCreate, Room: "GW01"}); err != nil {
		t.Fatalf("room.create: %v", err)
	}

	store, err := plugindata.Open(&file.FileUtils{Path: filepath.Join(t.TempDir(), "jti.bin")})
	if err != nil {
		t.Fatal(err)
	}
	gw, err := gateway.New(gateway.Config{
		BackendID:      "rwhps-plain",
		RoomCode:       "GW01",
		ControlAddr:    fmt.Sprintf("127.0.0.1:%d", controlPort),
		HelperReady:    backend.Ready,
		PendingTimeout: 5 * time.Second,
		// 无 TLSCert/TLSKey -> 明文传输; TrustMode 零值 = open.
	}, gateway.NewJtiStore(store))
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}

	go func() { _ = gw.ServeControl() }()

	an := rwnet.NewAbstractNet(gameserver.NewRwHps(core.ServerProtocol))
	an.SetGateway(gw)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", gamePort))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go an.HandleChannel(core.NewChannel(c))
		}
	}()
	gw.SetReady(true)

	stop := func() {
		_ = ln.Close()
		gw.Stop()
		room.SetDefault(nil)
		gameserver.AttachBackend(nil)
		_ = backend.Close()
	}
	return backend, gw, stop
}

// writePlainCascadeConfig 写 Cascade 配置: http 控制面 + trust_mode open +
// tls_mode auto, 且不提供任何 mTLS 材料.
func writePlainCascadeConfig(t *testing.T, dir, proxyKeyPath, controlURL, gameAddr string, cascadeGamePort, cascadeHTTPPort int) {
	t.Helper()
	cfg := fmt.Sprintf(`version: 2
game_listen_addr: "127.0.0.1:%d"
http_listen_addr: "127.0.0.1:%d"
default_backend_id: "rwhps-plain"
backends:
  - id: "rwhps-plain"
    control_url: "%s"
    game_address: "%s"
    prefixes: ["GW"]
    trust_mode: "open"
    tls_mode: "auto"
proxy_issuer: "cascade"
proxy_key_id: "proxy-key-e2e"
proxy_private_key_path: "%s"
health_interval_sec: 1
room_snapshot_interval_sec: 1
pending_session_timeout_sec: 5
`, cascadeGamePort, cascadeHTTPPort, controlURL, gameAddr, proxyKeyPath)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCascadePlainTransportE2E(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	jar := filepath.Join(root, "Helper", "build", "libs", "Helper.jar")
	if info, err := os.Stat(jar); err != nil || info.IsDir() {
		t.Skipf("Helper.jar 未构建, 跳过明文联调: %s", jar)
	}
	java := findJDK17Java(t)

	km := genKeyMaterial(t) // 只用 proxyKeyPath (Ed25519 签名密钥)
	cascadeBin := buildCascade(t, t.TempDir())

	gamePort := freePort(t)
	controlPort := freePort(t)
	cascadeGamePort := freePort(t)
	cascadeHTTPPort := freePort(t)

	backend, gw, stopBackend := startPlainGoBackend(t, java, jar, gamePort, controlPort)
	defer stopBackend()

	gameAddr := fmt.Sprintf("127.0.0.1:%d", gamePort)
	controlURL := fmt.Sprintf("http://127.0.0.1:%d", controlPort) // 明文
	waitForPort(t, gameAddr, 10*time.Second)

	cascadeDir := t.TempDir()
	copyCascadeMigrations(t, cascadeDir)
	writePlainCascadeConfig(t, cascadeDir, km.proxyKeyPath, controlURL, gameAddr, cascadeGamePort, cascadeHTTPPort)
	cascadeCmd := exec.Command(cascadeBin)
	cascadeCmd.Dir = cascadeDir
	cascadeLog := filepath.Join(cascadeDir, "cascade.log")
	lf, err := os.Create(cascadeLog)
	if err != nil {
		t.Fatal(err)
	}
	cascadeCmd.Stdout = lf
	cascadeCmd.Stderr = lf
	if err := cascadeCmd.Start(); err != nil {
		t.Fatalf("启动 Cascade: %v", err)
	}
	defer func() {
		_ = cascadeCmd.Process.Kill()
		_, _ = cascadeCmd.Process.Wait()
		_ = lf.Close()
		if data, err := os.ReadFile(cascadeLog); err == nil {
			t.Logf("[Cascade 日志]\n%s", tail(data, 2000))
		}
	}()

	cascadeGameAddr := fmt.Sprintf("127.0.0.1:%d", cascadeGamePort)
	waitForPort(t, cascadeGameAddr, 15*time.Second)
	waitCascadeReady(t, cascadeHTTPPort, 30*time.Second)

	const roomCode = "GW01"
	conn, err := net.Dial("tcp", cascadeGameAddr)
	if err != nil {
		t.Fatalf("连 Cascade: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write(rwFrame(160, preregisterBody(roomCode))); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	typeInt, body := readFrame(t, conn)
	if typeInt != 161 {
		t.Fatalf("期望 Preregister 应答 (161), 得到 %d (%d 字节)", typeInt, len(body))
	}
	t.Logf("[E2E-明文] 客户端经 Cascade(明文) 收到引擎 Preregister 应答 (161), %d 字节", len(body))

	const playerUUID = "8899aabbccddeeff"
	if _, err := conn.Write(rwFrame(110, registerBody("PlainPlayer", playerUUID))); err != nil {
		t.Fatal(err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	diagType, diagBody := readFrame(t, conn)
	t.Logf("[E2E-明文] RegisterPlayer 后首帧: 类型=%d %d 字节 %q", diagType, len(diagBody), string(diagBody))

	deadline := time.Now().Add(20 * time.Second)
	bound := false
	for time.Now().Before(deadline) && !bound {
		for _, id := range gw.Registry().IDs() {
			if s := gw.Registry().Get(id); s != nil && s.PlayerID() == playerUUID {
				bound = true
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !bound {
		t.Fatal("身份未在限期内绑定 (明文链路未通)")
	}
	t.Logf("[E2E-明文] 身份绑定成功: 会话已关联玩家 UUID %s", playerUUID)

	runRealGameToSettlement(t, backend)
}
```

The helpers `findJDK17Java`, `genKeyMaterial`, `buildCascade`, `freePort`,
`waitForPort`, `waitCascadeReady`, `copyCascadeMigrations`, `rwFrame`,
`preregisterBody`, `registerBody`, `readFrame`, `tail`, and
`runRealGameToSettlement` are the ones already shipped in
`cascade_e2e_test.go` / `cascade_e2e_helpers_test.go` in the RW-HPS
repository; this file reuses them unchanged.
