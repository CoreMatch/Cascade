// Package main —— Cascade 插件参考实现（模板）。
//
// 实现 cascade-plugin/v1 契约的插件侧最小完整服务：manifest 协商、
// 双向 HMAC 签名、事件分发、可否决事件应答。只用 Go 标准库，整个目录
// 可以原样拷到任何独立仓库编译。机制与 Avalon docs/plugins/template
// 同构（仅协议标识与签名头名不同：cascade-plugin/v1、X-Cascade-*）。
//
// 阅读顺序：plugin.go（契约机制）-> main.go（route-watcher 怎么装配）。
// 配套文档：docs/plugins/（Cascade 仓）。
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 契约 "Authentication"：双向 HMAC-SHA256。
// 请求与应答都携带时间戳 + 签名头，签名覆盖 "时间戳.原始正文"。
const (
	headerTimestamp = "X-Cascade-Timestamp"
	headerSignature = "X-Cascade-Signature"

	// signaturePrefix 签名值前缀（版本化，将来换算法时递增）
	signaturePrefix = "v1="

	// maxClockSkewSec 时间戳偏移容忍（秒），与 Cascade 侧一致
	maxClockSkewSec = 300

	// maxBodyBytes 请求正文读取上限（失败隔离：畸形大请求不得吃内存）
	maxBodyBytes = 1 << 20

	// ProtocolID manifest 的 protocol 字段，Cascade 要求精确匹配
	ProtocolID = "cascade-plugin/v1"
	// ManifestSchemaValue manifest 的 schema 字段，Cascade 要求精确匹配
	ManifestSchemaValue = "cascade-plugin/manifest"
)

// Sign 计算 body 在给定密钥与时间戳下的签名值（含 "v1=" 前缀）：
//
//	v1=hex(HMAC-SHA256("<unix秒>.<原始正文>", secret))
//
// GET /v1/manifest 的正文按空串参与签名。
func Sign(secret []byte, unixSec int64, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%d.", unixSec)
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}

// Verify 校验对端（Cascade）请求的签名头：时间戳新鲜 + 恒定时间比较。
// 任一环节不满足都应拒绝请求（返回 401）——验签失败不是错误而是攻击面。
func Verify(secret []byte, tsHeader, sigHeader string, body []byte) error {
	if tsHeader == "" || sigHeader == "" {
		return fmt.Errorf("missing signature headers")
	}
	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		return fmt.Errorf("bad timestamp header %q", tsHeader)
	}
	if d := time.Now().Unix() - ts; d > maxClockSkewSec || d < -maxClockSkewSec {
		return fmt.Errorf("timestamp skew %ds exceeds ±%ds", d, maxClockSkewSec)
	}
	if !hmac.Equal([]byte(Sign(secret, ts, body)), []byte(sigHeader)) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// Verdict 是否决类事件的应答形状。allowed=false 时 reason 必填且可读：
// Cascade 会把无理由的拒绝当作畸形应答 fail-open 处理（等于否决没生效）。
type Verdict struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

// Handler 处理一个事件，返回（HTTP 状态码，应答正文）。
//   - 否决类事件（player.route）：应答正文必须是 Verdict 的 JSON。
//   - 观察类事件（player.registered / player.routed）：任意 2xx 都是确认，
//     正文被忽略（惯例回 {}）。观察档失败即弃，不重试。
type Handler func(body []byte) (int, []byte)

// EventCapability manifest 里单个事件的能力声明
type EventCapability struct {
	// Versions 支持的事件版本；当前 Cascade 只发版本 1
	Versions []int `json:"versions"`
	// Veto 双模事件（player.route）声明希望进入否决模式。是否生效还
	// 取决于运营在 Cascade 侧的授权（Plugins.json vetoEvents，默认拒绝）。
	Veto bool `json:"veto,omitempty"`
}

// Plugin 是按契约实现的最小插件服务器（http.Handler）
type Plugin struct {
	// ID / Version manifest 的自报身份，自由字符串
	ID      string
	Version string
	// TimeoutMs 自报处理预算（毫秒）；Cascade 用自己的上限取 min 封顶。
	// 0 = 用 Cascade 的上限。
	TimeoutMs int

	secret []byte

	mu       sync.RWMutex
	events   map[string]EventCapability
	handlers map[string]Handler
}

// NewPlugin 构造插件服务器；secret 是与 Cascade 共享的密钥原始字节
// （Plugins.json 里配的是它的 hex）。
func NewPlugin(id, version string, secret []byte) *Plugin {
	return &Plugin{
		ID:       id,
		Version:  version,
		secret:   secret,
		events:   map[string]EventCapability{},
		handlers: map[string]Handler{},
	}
}

// Handle 订阅一个事件并注册处理器。
//
// 订阅 = manifest 里声明该事件（versions=[1]）；veto 为 true 时额外声明
// 否决能力（还须运营授权才真正参与否决咨询，否则按观察档收到同一事件）。
// 同一事件重复注册覆盖。
func (p *Plugin) Handle(event string, veto bool, h Handler) {
	p.mu.Lock()
	p.events[event] = EventCapability{Versions: []int{1}, Veto: veto}
	p.handlers[event] = h
	p.mu.Unlock()
}

// ServeHTTP 路由：GET /v1/manifest，POST /v1/events/{name}
func (p *Plugin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/manifest":
		p.serveManifest(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/events/"):
		p.serveEvent(w, r, strings.TrimPrefix(r.URL.Path, "/v1/events/"))
	default:
		http.NotFound(w, r)
	}
}

// serveManifest 返回签名的自我描述（协商入口）
func (p *Plugin) serveManifest(w http.ResponseWriter, r *http.Request) {
	if err := p.verifyRequest(r, nil); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	p.mu.RLock()
	manifest := struct {
		Schema         string                     `json:"schema"`
		Protocol       string                     `json:"protocol"`
		ID             string                     `json:"id"`
		Version        string                     `json:"version"`
		Events         map[string]EventCapability `json:"events"`
		TimeoutMs      int                        `json:"timeoutMs,omitempty"`
		MaxConcurrency int                        `json:"maxConcurrency"`
	}{
		Schema:         ManifestSchemaValue,
		Protocol:       ProtocolID,
		ID:             p.ID,
		Version:        p.Version,
		Events:         p.events,
		TimeoutMs:      p.TimeoutMs,
		MaxConcurrency: 1, // 保留字段：Cascade 恒按单 worker 对待
	}
	p.mu.RUnlock()

	body, _ := json.Marshal(manifest)
	p.writeSigned(w, http.StatusOK, body)
}

// serveEvent 校验签名后分发到注册的处理器
func (p *Plugin) serveEvent(w http.ResponseWriter, r *http.Request, name string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || int64(len(body)) > maxBodyBytes {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	if err := p.verifyRequest(r, body); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	p.mu.RLock()
	h := p.handlers[name]
	p.mu.RUnlock()
	if h == nil {
		// 未订阅的事件不应到达；明确拒绝便于排查配置漂移
		http.Error(w, "event not subscribed: "+name, http.StatusNotFound)
		return
	}
	status, resp := h(body)
	if resp == nil {
		resp = []byte(`{}`)
	}
	p.writeSigned(w, status, resp)
}

// verifyRequest 校验 Cascade 请求的签名（manifest 的 body 传 nil = 空正文）
func (p *Plugin) verifyRequest(r *http.Request, body []byte) error {
	return Verify(p.secret, r.Header.Get(headerTimestamp), r.Header.Get(headerSignature), body)
}

// writeSigned 写应答并签名——每个应答都必须带签名头，否则 Cascade 按
// 畸形应答处理（否决档 fail-open，观察档计失败丢弃）。
func (p *Plugin) writeSigned(w http.ResponseWriter, status int, body []byte) {
	ts := time.Now().Unix()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set(headerTimestamp, strconv.FormatInt(ts, 10))
	w.Header().Set(headerSignature, Sign(p.secret, ts, body))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// VerdictJSON 是 Handler 返回否决应答的便捷构造
func VerdictJSON(v Verdict) []byte {
	b, _ := json.Marshal(v)
	return b
}

// DecodeHexSecret 把 Plugins.json 里的 hex 密钥解码成字节
func DecodeHexSecret(hexSecret string) ([]byte, error) {
	raw, err := hex.DecodeString(hexSecret)
	if err != nil || len(raw) < 16 {
		return nil, fmt.Errorf("secret 必须是至少 16 字节的 hex")
	}
	return raw, nil
}
