package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Manifest 是插件的自我描述，由插件在 GET {base}/v1/manifest 提供
// （形状与 Avalon avalon-plugin manifest 一致）
type Manifest struct {
	Schema   string                     `json:"schema"`
	Protocol string                     `json:"protocol"`
	ID       string                     `json:"id"`
	Version  string                     `json:"version"`
	Events   map[string]EventCapability `json:"events"`
	// TimeoutMs 插件自报处理预算；Cascade 用自己的上限取 min 封顶
	TimeoutMs int `json:"timeoutMs"`
	// MaxConcurrency 保留字段：实际恒为单 worker（保序），>1 按 1 处理
	MaxConcurrency int `json:"maxConcurrency"`
}

// EventCapability 单个事件的受支持版本与模式声明
type EventCapability struct {
	Versions []int `json:"versions"`
	// Veto 仅对双模事件（player.route）有意义：插件声明希望进入否决
	// 模式。是否生效还取决于运营授权（Entry.VetoEvents，默认拒绝）；
	// 未授权时按观察模式投递。
	Veto bool `json:"veto"`
}

// supports 判断插件是否支持某事件的发送版本，返回协商出的版本号
// （无交集返回 0）
func (m *Manifest) supports(eventName string) int {
	if m == nil {
		return 0
	}
	cap, ok := m.Events[eventName]
	if !ok {
		return 0
	}
	for _, v := range cap.Versions {
		if v == emitVersion {
			return emitVersion
		}
	}
	return 0
}

func (m *Manifest) validate() error {
	if m.Schema != ManifestSchema {
		return fmt.Errorf("manifest schema %q != %q", m.Schema, ManifestSchema)
	}
	if m.Protocol != ProtocolV1 {
		return fmt.Errorf("manifest protocol %q != %q", m.Protocol, ProtocolV1)
	}
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("manifest id is empty")
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("manifest version is empty")
	}
	return nil
}

// maxResponseBytes 应答正文读取上限（失败隔离：畸形大应答不得吃内存）
const maxResponseBytes = 64 * 1024

// fetchManifest 拉取并校验插件 manifest（签名双向校验）。
//
// 成功时返回：协商好的事件版本表（事件名 -> 版本）、插件声明了 veto
// 能力的事件集、以及 manifest 本身。
func (p *Plugin) fetchManifest(ctx context.Context) (map[string]int, map[string]bool, *Manifest, error) {
	url := strings.TrimRight(p.entry.BaseURL, "/") + "/v1/manifest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	signRequest(req, p.secrets[0], nil)

	resp, err := p.hub.client.Do(req)
	if err != nil {
		return nil, nil, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, nil, nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, nil, nil, fmt.Errorf("manifest response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, nil, fmt.Errorf("manifest status %d", resp.StatusCode)
	}
	if err := verifyResponse(p.secrets, resp, body); err != nil {
		return nil, nil, nil, fmt.Errorf("manifest response: %w", err)
	}

	m := &Manifest{}
	if err := json.Unmarshal(body, m); err != nil {
		return nil, nil, nil, fmt.Errorf("manifest parse: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, nil, nil, err
	}

	negotiated := make(map[string]int, len(allEventNames))
	vetoDeclared := make(map[string]bool, len(allEventNames))
	for _, name := range allEventNames {
		if v := m.supports(name); v > 0 {
			negotiated[name] = v
			if ec, ok := m.Events[name]; ok && ec.Veto {
				vetoDeclared[name] = true
			}
		}
	}
	return negotiated, vetoDeclared, m, nil
}
