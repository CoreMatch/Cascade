// route-watcher —— 最小可跑的 Cascade 插件（对局路由观察/否决示例）。
//
// 运行：
//
//	go run . -addr 127.0.0.1:9801 -secret <与 Cascade 共享的 hex 密钥>
//
// 然后在 Cascade 工作目录的 Plugins.json 里登记（见 README.md）；
// Cascade 启动/重启后会来拉 manifest 完成握手，之后按订阅推送事件。
// 插件不需要主动连接 Cascade —— 方向是 Cascade 调用插件。
//
// 演练开关（故障演练用，正常运行都不开）：
//
//	-deny           否决全部 player.route（玩家留在大厅并收到理由）
//	-hang-ms N      player.route 处理前睡 N 毫秒（超时演练：超过
//	                Plugins.json admissionTimeoutMs 时 Cascade fail-open）
//	-crash-after N  处理满 N 个事件后退出进程（崩溃演练：Cascade 标记
//	                插件失联，主链路不受影响，重启插件后自动重新协商）
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9801", "监听地址")
	secretHex := flag.String("secret", "", "与 Cascade 共享的密钥 (hex, 与 Plugins.json 的 secrets 一致)")
	deny := flag.Bool("deny", false, "演练: 否决全部 player.route 转向")
	hangMs := flag.Int("hang-ms", 0, "演练: player.route 处理前睡眠毫秒数 (制造超时)")
	crashAfter := flag.Int64("crash-after", 0, "演练: 处理满 N 个事件后进程退出 (0=不退出)")
	flag.Parse()

	if *secretHex == "" {
		log.Fatal("必须提供 -secret (hex)")
	}
	secret, err := DecodeHexSecret(*secretHex)
	if err != nil {
		log.Fatalf("secret: %v", err)
	}

	p := NewPlugin("route-watcher", "0.1.0", secret)
	var handled atomic.Int64
	afterEvent := func(name string) {
		n := handled.Add(1)
		if *crashAfter > 0 && n >= *crashAfter {
			log.Printf("[drill] crash-after=%d reached at event #%d (%s), exiting now", *crashAfter, n, name)
			os.Exit(1) // 故意不优雅退出：演练插件崩溃
		}
	}

	// --- player.registered (观察档): 身份绑定完成 ---
	p.Handle("player.registered", false, func(body []byte) (int, []byte) {
		defer afterEvent("player.registered")
		var ev struct {
			EventID string `json:"eventId"`
			Player  struct {
				PlayerID      string `json:"playerId"`
				Name          string `json:"name"`
				Authenticated bool   `json:"authenticated"`
			} `json:"player"`
			Backend struct {
				BackendID string `json:"backendId"`
			} `json:"backend"`
		}
		if err := json.Unmarshal(body, &ev); err != nil {
			return http.StatusBadRequest, nil
		}
		log.Printf("[player.registered] id=%s player=%s(%s) backend=%s authenticated=%v",
			ev.EventID, ev.Player.Name, ev.Player.PlayerID, ev.Backend.BackendID, ev.Player.Authenticated)
		return http.StatusOK, []byte(`{}`)
	})

	// --- player.route (双模: 授权后参与否决): 分配转向执行前 ---
	// 应答必须是 Verdict; allowed=false 时 reason 必填 (无理由会被当畸形
	// 应答 fail-open, 否决不生效)。
	p.Handle("player.route", true, func(body []byte) (int, []byte) {
		defer afterEvent("player.route")
		if *hangMs > 0 {
			log.Printf("[drill] hanging %dms before answering player.route", *hangMs)
			sleepMs(*hangMs)
		}
		var ev struct {
			EventID string `json:"eventId"`
			Player  struct {
				PlayerID string `json:"playerId"`
			} `json:"player"`
			Decision struct {
				BackendID  string `json:"backendId"`
				JoinTarget string `json:"joinTarget"`
				GroupID    int64  `json:"groupId"`
			} `json:"decision"`
		}
		if err := json.Unmarshal(body, &ev); err != nil {
			return http.StatusBadRequest, nil
		}
		log.Printf("[player.route] id=%s player=%s -> backend=%s join_target=%s group=%d",
			ev.EventID, ev.Player.PlayerID, ev.Decision.BackendID, ev.Decision.JoinTarget, ev.Decision.GroupID)
		if *deny {
			return http.StatusOK, VerdictJSON(Verdict{Allowed: false, Reason: "route-watcher 演练否决: 维护中"})
		}
		return http.StatusOK, VerdictJSON(Verdict{Allowed: true})
	})

	// --- player.routed (观察档): 178 跳转包已发出 ---
	p.Handle("player.routed", false, func(body []byte) (int, []byte) {
		defer afterEvent("player.routed")
		var ev struct {
			EventID string `json:"eventId"`
			Player  struct {
				PlayerID string `json:"playerId"`
			} `json:"player"`
			Backend struct {
				BackendID string `json:"backendId"`
			} `json:"backend"`
			Target string `json:"target"`
		}
		if err := json.Unmarshal(body, &ev); err != nil {
			return http.StatusBadRequest, nil
		}
		log.Printf("[player.routed] id=%s player=%s backend=%s target=%s",
			ev.EventID, ev.Player.PlayerID, ev.Backend.BackendID, ev.Target)
		return http.StatusOK, []byte(`{}`)
	})

	log.Printf("route-watcher listening on %s (id=%s deny=%v hang=%dms crash-after=%d)",
		*addr, p.ID, *deny, *hangMs, *crashAfter)
	if err := http.ListenAndServe(*addr, p); err != nil {
		log.Fatal(err)
	}
}

func sleepMs(ms int) {
	<-time.After(time.Duration(ms) * time.Millisecond)
}
