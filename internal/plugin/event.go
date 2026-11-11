package plugin

import (
	"crypto/rand"
	"encoding/json"
	"time"
)

// 事件信封与 Avalon 同形：eventId / event / eventVersion / occurredAt +
// 宿主段 + 事件段。eventId 生成一次即固定，重试与重放复用同一 ID
//（插件据此去重）。

// ulidEncoding Crockford base32（不含 I L O U）
const ulidEncoding = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newEventID 生成 "evt_" + 26 字符 ULID（48bit 毫秒时间 + 80bit 随机）
func newEventID(now time.Time) string {
	var b [26]byte

	ms := now.UnixMilli()
	for i := 9; i >= 0; i-- {
		b[i] = ulidEncoding[ms&31]
		ms >>= 5
	}

	var rnd [10]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		// crypto/rand 失败只在系统熵池异常时发生；用时间戳填充保持可用
		//（唯一性由时间前缀与低碰撞概率兜底，事件 ID 不是安全边界）
		for i := range rnd {
			rnd[i] = byte(now.UnixNano() >> (i * 3))
		}
	}
	b[10] = ulidEncoding[(rnd[0]&248)>>3]
	b[11] = ulidEncoding[((rnd[0]&7)<<2)|((rnd[1]&192)>>6)]
	b[12] = ulidEncoding[(rnd[1]&62)>>1]
	b[13] = ulidEncoding[((rnd[1]&1)<<4)|((rnd[2]&240)>>4)]
	b[14] = ulidEncoding[(rnd[2]&15)>>1]
	b[15] = ulidEncoding[(rnd[3]&124)>>2]
	b[16] = ulidEncoding[((rnd[3]&3)<<3)|((rnd[4]&224)>>5)]
	b[17] = ulidEncoding[rnd[4]&31]
	b[18] = ulidEncoding[(rnd[5]&248)>>3]
	b[19] = ulidEncoding[((rnd[5]&7)<<2)|((rnd[6]&192)>>6)]
	b[20] = ulidEncoding[(rnd[6]&62)>>1]
	b[21] = ulidEncoding[((rnd[6]&1)<<4)|((rnd[7]&240)>>4)]
	b[22] = ulidEncoding[((rnd[7]&15)<<1)|((rnd[8]&128)>>7)]
	b[23] = ulidEncoding[(rnd[8]&124)>>2]
	b[24] = ulidEncoding[((rnd[8]&3)<<3)|((rnd[9]&224)>>5)]
	b[25] = ulidEncoding[rnd[9]&31]

	return "evt_" + string(b[:])
}

// ProxyInfo 信封里的宿主段（对应 Avalon 的 server 段）
type ProxyInfo struct {
	// ProxyID 取自配置 proxy_issuer（运营可辨认的实例标识）
	ProxyID string `json:"proxyId"`
	// PublicHost 178 跳转目标主机（可为空 = 跳转未启用）
	PublicHost string `json:"publicHost,omitempty"`
	// HostVersion 插件协议宿主版本（可观测用）
	HostVersion string `json:"hostVersion"`
}

// PlayerInfo 信封里的玩家段。插件是运营显式配置的可信基础设施
// （密钥鉴权），与 Avalon 插件事件携带玩家档案同一信任级别。
type PlayerInfo struct {
	PlayerID      string `json:"playerId"`
	Name          string `json:"name"`
	Authenticated bool   `json:"authenticated"`
	// HAUserUUID 已绑定账号的 HRPAuth 账号 UUID；游客为空
	HAUserUUID string `json:"haUserUuid,omitempty"`
}

// RouteDecision player.route 载荷里的分配决策段
type RouteDecision struct {
	// Source 决策来源，当前恒为 "mca"
	Source string `json:"source"`
	// BackendID 决策要送往的后端 ID
	BackendID string `json:"backendId"`
	// JoinTarget MCA 成局携带的子服地址（映射表的键）
	JoinTarget string `json:"joinTarget,omitempty"`
	GroupID    int64  `json:"groupId,omitempty"`
	MatchType  string `json:"matchType,omitempty"`
	TeamID     string `json:"teamId,omitempty"`
}

type eventEnvelope struct {
	EventID      string         `json:"eventId"`
	Event        string         `json:"event"`
	EventVersion int            `json:"eventVersion"`
	OccurredAt   string         `json:"occurredAt"`
	Proxy        ProxyInfo      `json:"proxy"`
	Player       PlayerInfo     `json:"player"`
	Backend      *backendRef    `json:"backend,omitempty"`
	Decision     *RouteDecision `json:"decision,omitempty"`
	Target       string         `json:"target,omitempty"`
}

type backendRef struct {
	BackendID string `json:"backendId"`
}

func buildEnvelope(eventName string, proxy ProxyInfo, player PlayerInfo, now time.Time) *eventEnvelope {
	return &eventEnvelope{
		EventID:      newEventID(now),
		Event:        eventName,
		EventVersion: emitVersion,
		OccurredAt:   now.UTC().Format(time.RFC3339Nano),
		Proxy:        proxy,
		Player:       player,
	}
}

func marshalEvent(ev *eventEnvelope) string {
	b, err := json.Marshal(ev)
	if err != nil {
		// 信封全部是可直接序列化的字段；此处不可达
		return "{}"
	}
	return string(b)
}

func buildPlayerRegisteredEvent(proxy ProxyInfo, player PlayerInfo, backendID string, now time.Time) (string, string) {
	ev := buildEnvelope(EventPlayerRegistered, proxy, player, now)
	ev.Backend = &backendRef{BackendID: backendID}
	return ev.EventID, marshalEvent(ev)
}

func buildPlayerRouteEvent(proxy ProxyInfo, player PlayerInfo, backendID string, decision RouteDecision, now time.Time) (string, string) {
	ev := buildEnvelope(EventPlayerRoute, proxy, player, now)
	ev.Backend = &backendRef{BackendID: backendID}
	ev.Decision = &decision
	return ev.EventID, marshalEvent(ev)
}

func buildPlayerRoutedEvent(proxy ProxyInfo, player PlayerInfo, backendID, target string, now time.Time) (string, string) {
	ev := buildEnvelope(EventPlayerRouted, proxy, player, now)
	ev.Backend = &backendRef{BackendID: backendID}
	ev.Target = target
	return ev.EventID, marshalEvent(ev)
}
