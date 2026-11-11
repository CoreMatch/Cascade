package plugin

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"cascade/internal/paths"
)

// 默认阈值（对齐 Avalon ADR-0006 "Sync/async semantics"）
const (
	DefaultAdmissionTimeoutMs = 2500
	DefaultDeliveryTimeoutMs  = 10000
	DefaultOutboxCapacity     = 128

	// minSecretBytes 共享密钥最小长度（16 字节 = 32 个 hex 字符）
	minSecretBytes = 16

	// pluginsFileName 插件清单文件名（工作目录，其次可执行文件旁）
	pluginsFileName = "Plugins.json"
)

// Config 对应 Plugins.json（形状与 Avalon /data/Plugins.json 同构；
// 无 deliver 档，故没有 maxEventAgeHours；无对局内否决，故没有
// gameplayVetoTimeoutMs）
type Config struct {
	// Version 配置 schema 版本，当前必须为 1
	Version int `json:"version"`

	// AdmissionTimeoutMs player.route 单次否决调用与启动期 manifest
	// 协商的绝对上限
	AdmissionTimeoutMs int `json:"admissionTimeoutMs"`
	// DeliveryTimeoutMs 观察档事件单次投递尝试的上限
	DeliveryTimeoutMs int `json:"deliveryTimeoutMs"`
	// OutboxCapacity 每插件内存队列容量，满则丢最旧
	OutboxCapacity int `json:"outboxCapacity"`

	Entries []Entry `json:"entries"`
}

// Entry 单个插件的运营配置
type Entry struct {
	// Name 插件名，唯一，用于日志与状态
	Name string `json:"name"`
	// BaseURL 插件 HTTP 服务基址，例如 http://127.0.0.1:9801
	BaseURL string `json:"baseUrl"`
	// Secrets 共享密钥列表（hex）；第一个为签名用主密钥，全部用于验签
	//（列表语义支持轮换重叠期）
	Secrets []string `json:"secrets"`
	// Enabled 是否启用；缺省（null）视为 true，false 显式停用
	Enabled *bool `json:"enabled"`
	// VetoEvents 否决授权清单：仅 player.route 可授权（默认拒绝）。
	// 插件还须在 manifest 里声明对应事件的 veto 能力，两者同时满足才
	// 进入否决模式，否则按观察模式投递。
	VetoEvents []string `json:"vetoEvents"`
}

func (e Entry) enabled() bool { return e.Enabled == nil || *e.Enabled }

// NewConfig 创建带默认值的配置（空 entries）
func NewConfig() Config {
	return Config{
		Version:            1,
		AdmissionTimeoutMs: DefaultAdmissionTimeoutMs,
		DeliveryTimeoutMs:  DefaultDeliveryTimeoutMs,
		OutboxCapacity:     DefaultOutboxCapacity,
	}
}

// LoadConfigFile 读取并校验 Plugins.json。
//
// 与 Avalon loadConfig 同一纪律：解析失败**不**回写默认值——插件是显式
// 配置，静默重置会把运营错误藏掉；文件缺失/为空才写默认值。返回解析后
// 的配置与清单文件实际路径（日志用）。
func LoadConfigFile() (Config, string, error) {
	path, found := paths.FirstExisting(pluginsFileName)
	if !found {
		path = pluginsFileName
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := NewConfig()
			if werr := writeConfig(path, cfg); werr != nil {
				return cfg, path, fmt.Errorf("plugin: create %s: %w", path, werr)
			}
			return cfg, path, nil
		}
		return Config{}, path, fmt.Errorf("plugin: read %s: %w", path, err)
	}

	if strings.TrimSpace(string(data)) == "" {
		cfg := NewConfig()
		if werr := writeConfig(path, cfg); werr != nil {
			return cfg, path, fmt.Errorf("plugin: write %s: %w", path, werr)
		}
		return cfg, path, nil
	}

	cfg := Config{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, path, fmt.Errorf("plugin: parse %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return cfg, path, err
	}
	return cfg, path, nil
}

func writeConfig(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

// validate 校验并补齐默认值；任何非法项都是启动错误
func (c *Config) validate() error {
	if c.Version != 1 {
		return fmt.Errorf("plugin: unsupported Plugins.json version %d (want 1)", c.Version)
	}
	if c.AdmissionTimeoutMs <= 0 {
		c.AdmissionTimeoutMs = DefaultAdmissionTimeoutMs
	}
	if c.DeliveryTimeoutMs <= 0 {
		c.DeliveryTimeoutMs = DefaultDeliveryTimeoutMs
	}
	if c.OutboxCapacity <= 0 {
		c.OutboxCapacity = DefaultOutboxCapacity
	}

	seen := make(map[string]bool, len(c.Entries))
	for i, e := range c.Entries {
		if strings.TrimSpace(e.Name) == "" {
			return fmt.Errorf("plugin: entry %d: empty name", i)
		}
		if seen[e.Name] {
			return fmt.Errorf("plugin: duplicate entry name %q", e.Name)
		}
		seen[e.Name] = true

		u, err := url.Parse(e.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("plugin: entry %q: baseUrl must be an absolute http(s) URL, got %q", e.Name, e.BaseURL)
		}

		if len(e.Secrets) == 0 {
			return fmt.Errorf("plugin: entry %q: at least one secret is required", e.Name)
		}
		for j, s := range e.Secrets {
			raw, err := hex.DecodeString(s)
			if err != nil || len(raw) < minSecretBytes {
				return fmt.Errorf("plugin: entry %q: secret %d must be hex of at least %d bytes", e.Name, j, minSecretBytes)
			}
		}

		for _, name := range e.VetoEvents {
			if !vetoGrantEvents[name] {
				return fmt.Errorf("plugin: entry %q: vetoEvents contains unknown or non-vetoable event %q", e.Name, name)
			}
		}
	}
	return nil
}
