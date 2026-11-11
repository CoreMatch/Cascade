// Package plugin 实现 Cascade 的进程外插件扩展点，机制与 Avalon 的
// internal/plugin 同构（Avalon 侧设计记录见其 knowledge/0006 与
// HA-Contract avalon-plugin-v1；竞品调查结论见
// /home/lnb/Desktop/HA/rw/PLUGIN-SYSTEM-SURVEY-20261107.md：扩展面只在
// 治理面、插件是独立进程、版本闸门、内容寻址分发）。
//
// 插件是独立进程/服务，自己起 HTTP 服务；Cascade 是调用方，永不执行插件
// 代码，插件也永不链接 Cascade。方向固定：Cascade 调用插件，v1 没有
// 插件→Cascade 的调用。仅用 Go 标准库。
//
// 清单（Plugins.json，工作目录）缺失或 entries 为空时 hub 空转：不产生
// 任何网络 I/O，代理行为与未启用时完全一致。清单非法则启动显式失败
// （与 Cascade 配置纪律一致，不静默降级）。
//
// v1 事件目录（cascade-plugin/v1）：
//
//	player.registered  身份绑定完成        观察档（尽力投递，失败即弃）
//	player.route       分配转向执行前      双模：授权+声明者参与否决咨询，
//	                                       其余订阅者按观察档投递
//	player.routed      178 跳转包已发出    观察档
//
// 否决语义与 Avalon 一致：任一有效否决即否决（理由串联呈现）；超时、
// 不可达、畸形应答、无理由拒绝一律 fail-open 放行并计数告警；至多一次，
// 不重试。插件失败（崩溃/超时/畸形）永不阻塞玩家链路。
//
// 与 Avalon v1 的差异（均为范围裁剪，不是模型分歧）：
//   - 无 deliver 档（WAL 至少一次）：Cascade 当前没有必须不丢的异步
//     结算事件；观察档内存队列足够。需要时按 Avalon outbox 同形扩展。
//   - 无 gameplay 紧超时档：Cascade 不在对局指令路径上。
package plugin

// 协议与清单标识（对应 Avalon 的 avalon-plugin/v1 命名空间）
const (
	// ProtocolV1 manifest 的 protocol 字段，插件必须精确匹配
	ProtocolV1 = "cascade-plugin/v1"
	// ManifestSchema manifest 的 schema 字段
	ManifestSchema = "cascade-plugin/manifest"

	// emitVersion v1 中 Cascade 对全部事件只发版本 1
	emitVersion = 1

	// HostVersion 事件信封里宿主自报的版本（可观测用，不是协商依据）
	HostVersion = "1.0.0"
)

// 事件名（稳定标识符，用于清单、协商与端点路径 /v1/events/{name}）
const (
	// EventPlayerRegistered 玩家身份绑定完成（注册 + 后端会话绑定成功）
	EventPlayerRegistered = "player.registered"
	// EventPlayerRoute 分配路由即将执行（MCA 成局决策已产生、跳转包未发）。
	// 双模事件：否决授权 + manifest 声明者参与否决咨询，可拦截转向；
	// 其余订阅者按观察档收到同一载荷。
	EventPlayerRoute = "player.route"
	// EventPlayerRouted 跳转包（178）已发出，玩家正在被送往目标后端
	EventPlayerRouted = "player.routed"
)

// allEventNames 协商与投递共用的事件全集
var allEventNames = []string{
	EventPlayerRegistered,
	EventPlayerRoute,
	EventPlayerRouted,
}

// EventNames 返回事件全集副本（测试与文档用）
func EventNames() []string { return append([]string(nil), allEventNames...) }

// vetoGrantEvents 需要运营显式授权（Entry.VetoEvents）才可进入否决模式的
// 事件。player.route 是 v1 唯一可否决事件；订阅但未授权/未声明者按观察档
// 投递（双模语义与 Avalon 一致）。
var vetoGrantEvents = map[string]bool{
	EventPlayerRoute: true,
}

// Tier 是事件投递档位
type Tier int

const (
	// TierObserve 尽力投递：内存队列，失败即弃，不重试不重放
	TierObserve Tier = iota
)

// Decision 是否决咨询的结论（形状与 Avalon plugin.Decision 一致）
type Decision struct {
	// Allowed 是否放行
	Allowed bool
	// Reason 否决理由（已净化：去控制符、截断 200 rune、带插件名前缀）；
	// 放行时为空
	Reason string
	// Plugin 首个否决的插件名（放行时为空）
	Plugin string
}
