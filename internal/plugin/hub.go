package plugin

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// 退避参数（与 Avalon ADR-0006 一致）
const (
	initialManifestBackoff = 5 * time.Second
	maxManifestBackoff     = 5 * time.Minute
)

// Hub 持有全部已配置插件的运行时状态。
//
// 构造（New）之后语义不可变：插件列表固定，配置改动需重启生效
// （v1 不做热加载）。Close 停止全部 worker 并等待退出。
type Hub struct {
	cfg     Config
	proxy   ProxyInfo
	logger  *log.Logger
	plugins []*Plugin
	client  *http.Client

	stopOnce sync.Once
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

// Plugin 是单个插件的运行时：协商状态 + 队列 + 投递 worker + 计数器
type Plugin struct {
	entry    Entry
	secrets  [][]byte
	disabled bool
	// vetoGrant 运营授权的否决事件集（默认拒绝）
	vetoGrant map[string]bool
	hub       *Hub

	mu         sync.Mutex
	ready      bool
	manifest   *Manifest
	negotiated map[string]int
	// vetoDeclared manifest 里声明了 veto 能力的事件集（生效还须 vetoGrant）
	vetoDeclared map[string]bool

	queue  *queue
	notify chan struct{}

	delivered atomic.Int64
	dropped   atomic.Int64
	denied    atomic.Int64
	failOpen  atomic.Int64
}

// New 校验配置、并行做首轮 manifest 协商，并为每个启用的插件启动唯一的
// 投递 worker（单 worker = 该插件的投递严格按入队序）。
//
// 配置非法返回错误使启动失败——插件是显式配置，不静默降级。插件不可达
// **不**失败：标记 unready 后由 worker 后台重试协商。
func New(cfg Config, proxy ProxyInfo, logger *log.Logger) (*Hub, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	h := &Hub{
		cfg:    cfg,
		proxy:  proxy,
		logger: logger,
		stopCh: make(chan struct{}),
		// 3xx 不跟随：重定向后的请求不再携带原签名语义，一律按失败处理
		client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}

	for _, e := range cfg.Entries {
		p := &Plugin{
			entry:     e,
			disabled:  !e.enabled(),
			hub:       h,
			notify:    make(chan struct{}, 1),
			vetoGrant: make(map[string]bool, len(e.VetoEvents)),
			queue:     newQueue(cfg.OutboxCapacity),
		}
		for _, name := range e.VetoEvents {
			p.vetoGrant[name] = true
		}
		for _, s := range e.Secrets {
			raw, _ := hex.DecodeString(s) // validate 已保证可解码
			p.secrets = append(p.secrets, raw)
		}
		h.plugins = append(h.plugins, p)
	}

	// 首轮协商：并行，每个插件独立超时。失败只记日志：启动不得被不可达
	// 的插件拖死。
	var bootWG sync.WaitGroup
	for _, p := range h.plugins {
		if p.disabled {
			continue
		}
		bootWG.Add(1)
		go func(p *Plugin) {
			defer bootWG.Done()
			ctx, cancel := context.WithTimeout(context.Background(), h.admissionTimeout())
			defer cancel()
			negotiated, vetoDeclared, m, err := p.fetchManifest(ctx)
			if err != nil {
				h.logger.Printf("[plugin] %s: manifest negotiation failed at boot: %v (retrying in background)", p.entry.Name, err)
				return
			}
			p.applyManifest(m, negotiated, vetoDeclared)
		}(p)
	}
	bootWG.Wait()

	for _, p := range h.plugins {
		if p.disabled {
			h.logger.Printf("[plugin] %s: disabled by config", p.entry.Name)
			continue
		}
		h.wg.Add(1)
		go func(p *Plugin) {
			defer h.wg.Done()
			defer func() {
				if r := recover(); r != nil {
					h.logger.Printf("[plugin] ERROR %s worker panic: %v", p.entry.Name, r)
				}
			}()
			p.run()
		}(p)
	}
	h.loadSummary()
	return h, nil
}

// Close 停止全部 worker 并等待退出，随后打印每个插件的累计计数
func (h *Hub) Close() {
	h.stopOnce.Do(func() { close(h.stopCh) })
	h.wg.Wait()
	for _, p := range h.plugins {
		if p.disabled {
			continue
		}
		h.logger.Printf("[plugin] %s: shutdown summary delivered=%d dropped=%d denied=%d fail_open=%d queued=%d",
			p.entry.Name, p.delivered.Load(), p.dropped.Load(), p.denied.Load(), p.failOpen.Load(), p.queue.size())
	}
}

// Empty 报告是否没有任何启用的插件（调用方快速路径：零网络 I/O）
func (h *Hub) Empty() bool {
	if h == nil {
		return true
	}
	for _, p := range h.plugins {
		if !p.disabled {
			return false
		}
	}
	return true
}

func (h *Hub) stopped() bool {
	select {
	case <-h.stopCh:
		return true
	default:
		return false
	}
}

func (h *Hub) admissionTimeout() time.Duration {
	return time.Duration(h.cfg.AdmissionTimeoutMs) * time.Millisecond
}

func (h *Hub) deliveryTimeout() time.Duration {
	return time.Duration(h.cfg.DeliveryTimeoutMs) * time.Millisecond
}

// --- 插件运行时 ---

func (p *Plugin) isReady() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ready
}

func (p *Plugin) getManifest() *Manifest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.manifest
}

func (p *Plugin) negotiatedFor(eventName string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.negotiated[eventName]
	return v, ok
}

func (p *Plugin) markUnready() {
	p.mu.Lock()
	p.ready = false
	p.mu.Unlock()
}

// applyManifest 协商成功：置 ready，丢弃版本不匹配的积压事件，唤醒 worker
func (p *Plugin) applyManifest(m *Manifest, negotiated map[string]int, vetoDeclared map[string]bool) {
	p.mu.Lock()
	p.ready = true
	p.manifest = m
	p.negotiated = negotiated
	p.vetoDeclared = vetoDeclared
	p.mu.Unlock()

	if m.MaxConcurrency > 1 {
		p.hub.logger.Printf("[plugin] WARN %s: maxConcurrency=%d is reserved, using 1 worker", p.entry.Name, m.MaxConcurrency)
	}

	names := make([]string, 0, len(negotiated))
	for _, name := range allEventNames {
		if _, ok := negotiated[name]; ok {
			names = append(names, name)
		}
	}
	p.hub.logger.Printf("[plugin] %s ready: id=%s version=%s events=[%s]",
		p.entry.Name, m.ID, m.Version, strings.Join(names, ", "))
	p.wake()
}

// run 是单插件的投递主循环：未就绪时做 manifest 重试，就绪时按序投递。
// 观察档失败即弃（不重试——过期观察无价值）；连接级失败回到协商路径。
func (p *Plugin) run() {
	manifestBackoff := initialManifestBackoff

	for {
		if p.hub.stopped() {
			return
		}

		if !p.isReady() {
			ctx, cancel := context.WithTimeout(context.Background(), p.hub.admissionTimeout())
			negotiated, vetoDeclared, m, err := p.fetchManifest(ctx)
			cancel()
			if err != nil {
				p.hub.logger.Printf("[plugin] WARN %s: manifest retry failed: %v", p.entry.Name, err)
				if p.sleepOrStop(manifestBackoff) {
					return
				}
				manifestBackoff = minDuration(manifestBackoff*2, maxManifestBackoff)
				continue
			}
			manifestBackoff = initialManifestBackoff
			p.applyManifest(m, negotiated, vetoDeclared)
			continue
		}

		rec, ok := p.queue.peek()
		if !ok {
			if p.waitNotify() {
				return
			}
			continue
		}

		if _, ok := p.negotiatedFor(rec.Event); !ok {
			p.queue.pop()
			p.dropped.Add(1)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), p.hub.deliveryTimeout())
		err := p.deliver(ctx, rec)
		cancel()
		if err == nil {
			p.queue.pop()
			p.delivered.Add(1)
			continue
		}

		if isConnError(err) {
			// 连接级失败 = 进程可能已死/重启：回到协商路径（含其自身退避）
			p.hub.logger.Printf("[plugin] WARN %s: connection lost (%v), renegotiating manifest", p.entry.Name, err)
			p.markUnready()
			continue
		}
		// 观察档：尽力投递，失败即弃
		p.queue.pop()
		p.dropped.Add(1)
		p.hub.logger.Printf("[plugin] WARN %s: observe delivery of %s failed: %v (dropped)", p.entry.Name, rec.EventID, err)
	}
}

// deliver 投递一条事件并校验应答签名；2xx+验签通过才算成功
func (p *Plugin) deliver(ctx context.Context, rec outboxRecord) error {
	body := []byte(rec.Body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.eventURL(rec.Event), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	signRequest(req, p.secrets[0], body)

	resp, err := p.hub.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return err
	}
	if len(respBody) > maxResponseBytes {
		return fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	if err := verifyResponse(p.secrets, resp, respBody); err != nil {
		return fmt.Errorf("ack signature: %w", err)
	}
	return nil
}

func (p *Plugin) eventURL(eventName string) string {
	return strings.TrimRight(p.entry.BaseURL, "/") + "/v1/events/" + eventName
}

// sleepOrStop 睡 d 时长；期间收到停止信号返回 true
func (p *Plugin) sleepOrStop(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-p.hub.stopCh:
		return true
	case <-t.C:
		return false
	}
}

// waitNotify 空队列等待：被新事件唤醒返回 false，收到停止信号返回 true
func (p *Plugin) waitNotify() bool {
	select {
	case <-p.hub.stopCh:
		return true
	case <-p.notify:
		return false
	}
}

// wake 非阻塞唤醒 worker（缓冲 1，多次唤醒合并成一次）
func (p *Plugin) wake() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

func (p *Plugin) cappedTimeout(cap time.Duration) time.Duration {
	if m := p.getManifest(); m != nil && m.TimeoutMs > 0 {
		if self := time.Duration(m.TimeoutMs) * time.Millisecond; self < cap {
			return self
		}
	}
	return cap
}

// vetoConsulted 该插件是否参与某事件的否决咨询：须就绪由调用方保证；
// 这里检查协商、manifest 声明与运营授权三者齐备。
func (p *Plugin) vetoConsulted(eventName string) bool {
	if !vetoGrantEvents[eventName] {
		return false
	}
	p.mu.Lock()
	declared := p.vetoDeclared[eventName]
	p.mu.Unlock()
	return declared && p.vetoGrant[eventName]
}

// --- 否决档（veto）对外语义 ---

// AdmitPlayerRoute 分配转向前同步咨询（至多一次，fail-open）。
// 返回否决时调用方应取消转向并把理由呈现给玩家。
//
// 放行时同步把同一 eventId/正文的观察副本入队给全部订阅者（含刚参与过
// 否决咨询的插件，与 Avalon prebuilt 语义一致）；否决时转向不发生，
// 不产生观察副本。
func (h *Hub) AdmitPlayerRoute(player PlayerInfo, backendID string, decision RouteDecision) Decision {
	eventID, body := buildPlayerRouteEvent(h.proxy, player, backendID, decision, time.Now())
	d := h.admitCore(EventPlayerRoute, body, h.admissionTimeout())
	if d.Allowed {
		h.enqueueEvent(EventPlayerRoute, eventID, body)
	}
	return d
}

// HasVetoConsult 是否存在被授予某事件否决权的启用插件（快速路径：
// 无授权插件时不产生任何网络调用）
func (h *Hub) HasVetoConsult(eventName string) bool {
	if h == nil {
		return false
	}
	for _, p := range h.plugins {
		if !p.disabled && p.vetoGrant[eventName] {
			return true
		}
	}
	return false
}

// admitCore 并行咨询全部候选插件。
//
// 语义（与 Avalon 一致）：任一有效否决即否决（理由串联）；超时/不可达/
// 畸形应答/无理由拒绝一律 fail-open 放行并计数告警；未就绪插件跳过并计
// fail-open（它本应参与准入）。至多一次，不重试。
func (h *Hub) admitCore(eventName string, body string, capTimeout time.Duration) Decision {
	candidates := make([]*Plugin, 0, len(h.plugins))
	for _, p := range h.plugins {
		if p.disabled {
			continue
		}
		if !p.isReady() {
			if p.vetoGrant[eventName] {
				p.failOpen.Add(1)
				h.logger.Printf("[plugin] WARN %s: not ready, %s admission skipped (fail-open)", p.entry.Name, eventName)
			}
			continue
		}
		if _, ok := p.negotiatedFor(eventName); !ok {
			continue // 未订阅：不参与准入，也不算失败
		}
		if !p.vetoConsulted(eventName) {
			continue // 订阅但按观察模式参与：不进否决咨询
		}
		candidates = append(candidates, p)
	}
	if len(candidates) == 0 {
		return Decision{Allowed: true}
	}

	results := make([]admissionOutcome, len(candidates))
	var wg sync.WaitGroup
	for i, p := range candidates {
		wg.Add(1)
		go func(i int, p *Plugin) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					h.logger.Printf("[plugin] ERROR %s admission panic: %v", p.entry.Name, r)
				}
			}()
			results[i] = p.callVeto(eventName, body, capTimeout)
		}(i, p)
	}
	wg.Wait()

	var reasons []string
	var firstPlugin string
	for i, r := range results {
		if r.denied {
			candidates[i].denied.Add(1)
			reasons = append(reasons, candidates[i].entry.Name+": "+r.denyReason)
			if firstPlugin == "" {
				firstPlugin = candidates[i].entry.Name
			}
		}
	}
	if len(reasons) > 0 {
		return Decision{Allowed: false, Reason: strings.Join(reasons, "; "), Plugin: firstPlugin}
	}
	return Decision{Allowed: true}
}

// admissionOutcome 单插件否决调用结果（零值 = 放行贡献）
type admissionOutcome struct {
	denied     bool
	denyReason string
}

// callVeto 对单个插件执行一次否决调用；任何异常路径都 fail-open
func (p *Plugin) callVeto(eventName string, body string, capTimeout time.Duration) admissionOutcome {
	fail := func(err error) admissionOutcome {
		p.failOpen.Add(1)
		p.hub.logger.Printf("[plugin] WARN %s: %s admission failed open: %v", p.entry.Name, eventName, err)
		return admissionOutcome{}
	}

	timeout := p.cappedTimeout(capTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.eventURL(eventName), bytes.NewReader([]byte(body)))
	if err != nil {
		return fail(err)
	}
	req.Header.Set("Content-Type", "application/json")
	signRequest(req, p.secrets[0], []byte(body))

	resp, err := p.hub.client.Do(req)
	if err != nil {
		return fail(err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fail(err)
	}
	if len(respBody) > maxResponseBytes {
		return fail(fmt.Errorf("response exceeds %d bytes", maxResponseBytes))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fail(fmt.Errorf("status %d", resp.StatusCode))
	}
	if err := verifyResponse(p.secrets, resp, respBody); err != nil {
		return fail(fmt.Errorf("verdict signature: %w", err))
	}

	verdict := struct {
		Allowed *bool  `json:"allowed"`
		Reason  string `json:"reason"`
	}{}
	if err := json.Unmarshal(respBody, &verdict); err != nil {
		return fail(fmt.Errorf("verdict parse: %w", err))
	}
	if verdict.Allowed == nil {
		return fail(errors.New("verdict missing 'allowed'"))
	}
	if *verdict.Allowed {
		return admissionOutcome{}
	}
	reason := sanitizeReason(verdict.Reason)
	if reason == "" {
		return fail(errors.New("deny without readable reason"))
	}
	return admissionOutcome{denied: true, denyReason: reason}
}

// --- 观察档发射入口 ---

// EmitPlayerRegistered 玩家身份绑定完成通知（非阻塞入队）
func (h *Hub) EmitPlayerRegistered(player PlayerInfo, backendID string) {
	eventID, body := buildPlayerRegisteredEvent(h.proxy, player, backendID, time.Now())
	h.enqueueEvent(EventPlayerRegistered, eventID, body)
}

// EmitPlayerRouted 跳转包已发出通知（非阻塞入队）
func (h *Hub) EmitPlayerRouted(player PlayerInfo, backendID, target string) {
	eventID, body := buildPlayerRoutedEvent(h.proxy, player, backendID, target, time.Now())
	h.enqueueEvent(EventPlayerRouted, eventID, body)
}

// enqueueEvent 把已构造好的事件正文入队到全部相关插件（非阻塞）。
// 就绪且协商表里没有该事件的插件跳过；未就绪插件先入队，协商成功后补投
// （版本不匹配在投递时丢弃并计数）。
func (h *Hub) enqueueEvent(eventName, eventID, body string) {
	if len(h.plugins) == 0 {
		return
	}
	rec := outboxRecord{
		EventID:      eventID,
		Event:        eventName,
		EnqueuedAtMs: time.Now().UnixMilli(),
		Body:         body,
	}
	for _, p := range h.plugins {
		if p.disabled {
			continue
		}
		if p.isReady() {
			if _, ok := p.negotiatedFor(eventName); !ok {
				continue
			}
		}
		if dropped := p.queue.append(rec); dropped > 0 {
			p.dropped.Add(int64(dropped))
			h.logger.Printf("[plugin] WARN %s: queue full, dropped %d oldest event(s)", p.entry.Name, dropped)
		}
		p.wake()
	}
}

// --- 运维可见性 ---

// PluginStatus 是单个插件的只读状态快照
type PluginStatus struct {
	Name            string
	BaseURL         string
	Enabled         bool
	Ready           bool
	ManifestID      string
	ManifestVersion string
	Events          []string
	Queued          int
	Delivered       int64
	Dropped         int64
	Denied          int64
	FailOpen        int64
}

// Status 返回全部插件（含停用）的状态快照，按配置顺序
func (h *Hub) Status() []PluginStatus {
	out := make([]PluginStatus, 0, len(h.plugins))
	for _, p := range h.plugins {
		st := PluginStatus{
			Name:      p.entry.Name,
			BaseURL:   p.entry.BaseURL,
			Enabled:   !p.disabled,
			Ready:     p.isReady(),
			Queued:    p.queue.size(),
			Delivered: p.delivered.Load(),
			Dropped:   p.dropped.Load(),
			Denied:    p.denied.Load(),
			FailOpen:  p.failOpen.Load(),
		}
		if m := p.getManifest(); m != nil {
			st.ManifestID = m.ID
			st.ManifestVersion = m.Version
		}
		for _, name := range allEventNames {
			if _, ok := p.negotiatedFor(name); ok {
				st.Events = append(st.Events, name)
			}
		}
		out = append(out, st)
	}
	return out
}

// --- 工具 ---

// maxReasonRunes 否决理由呈现给玩家前的长度上限
const maxReasonRunes = 200

// sanitizeReason 去控制符、去首尾空白、按 rune 截断
func sanitizeReason(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if runes := []rune(s); len(runes) > maxReasonRunes {
		s = string(runes[:maxReasonRunes])
	}
	return s
}

// isConnError 判断是否连接级失败（进程死/拒连）：超时不算
func isConnError(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	return false
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// loadSummary 启动日志：无插件时保持安静（默认状态不打扰）
func (h *Hub) loadSummary() {
	if len(h.plugins) == 0 {
		return
	}
	for _, p := range h.plugins {
		switch {
		case p.disabled:
			continue // New 里已打印
		case p.isReady():
			// applyManifest 已打印 ready 行
		default:
			h.logger.Printf("[plugin] %s: not ready at boot, will retry in background", p.entry.Name)
		}
	}
}
