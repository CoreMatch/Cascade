# Cascade 进程外插件（cascade-plugin/v1）

Cascade 的扩展面是**进程外插件**：插件是独立进程，自己起 HTTP 服务；
Cascade 是调用方，永不执行插件代码，插件永不链接 Cascade。机制与
Avalon 的进程外插件系统（`avalon-plugin/v1`）同构，便于互相借鉴与
复用工具链。设计依据：竞品插件机制调查
（`/home/lnb/Desktop/HA/rw/PLUGIN-SYSTEM-SURVEY-20261107.md`：三家
进程内插件体系全部随项目冻结/消亡，扩展面放在治理面、进程隔离是
收敛方向）与 Avalon knowledge/0006。

## 文档

| 文件 | 内容 |
|---|---|
| [EVENT-CATALOG.md](EVENT-CATALOG.md) | 事件名、载荷字段、档位（契约正文） |
| [AUTH-AND-NEGOTIATION.md](AUTH-AND-NEGOTIATION.md) | 双向 HMAC 签名与 manifest 协商 |
| [FAILURE-MODES.md](FAILURE-MODES.md) | 每类失败的确定行为（插件永不卡住主链路） |
| [template/](template/) | route-watcher 参考实现（Go 标准库，可整体拷出） |

## 快速上手

1. 插件进程监听一个 HTTP 地址，实现 `GET /v1/manifest` 与
   `POST /v1/events/{name}`（见 [template/](template/)）。
2. 在 Cascade 工作目录放 `Plugins.json`：

```json
{
  "version": 1,
  "admissionTimeoutMs": 2500,
  "deliveryTimeoutMs": 10000,
  "outboxCapacity": 128,
  "entries": [
    {
      "name": "route-watcher",
      "baseUrl": "http://127.0.0.1:9801",
      "secrets": ["<32+位 hex 共享密钥>"],
      "enabled": true,
      "vetoEvents": ["player.route"]
    }
  ]
}
```

3. 启动插件，再启动/重启 Cascade。Cascade 启动时并行拉取每个插件的
   manifest 完成握手；插件不可达不阻塞启动，后台按 5s→5min 退避重试。

配置纪律与 Cascade 本体一致：**清单缺失/为空 = 零插件、零回归**；
**清单非法 = 启动显式失败**，绝不静默降级。

## 与 Avalon 插件体系的对应关系

| 机制 | Avalon | Cascade |
|---|---|---|
| 清单文件 | `/data/Plugins.json` | 工作目录 `Plugins.json` |
| 协议标识 | `avalon-plugin/v1` | `cascade-plugin/v1` |
| 签名头 | `X-Avalon-Timestamp/Signature` | `X-Cascade-Timestamp/Signature` |
| 协商 | `GET {base}/v1/manifest` | 相同 |
| 投递端点 | `POST {base}/v1/events/{name}` | 相同 |
| 档位 | observe / veto / deliver(WAL) | observe / veto（deliver 为 backlog） |
| 否决授权 | `vetoEvents` + manifest 声明双闸门 | 相同 |
| 失败语义 | veto fail-open、observe 失败即弃 | 相同 |

## 部署方式

- 插件与 Cascade 分进程部署，可独立启停/替换/升级；插件重启后 Cascade
  自动重新协商（连接级失败 → 标记 unready → 后台重拉 manifest）。
- 插件无状态要求：事件携带 `eventId`，观察档可能因队列满被丢弃，
  插件按自身需求容忍缺失（v1 无重放）。
- 本机栈形态：`stack/up.sh` 会起 `local/cascade-plugin/route-watcher`
  （由本仓 `docs/plugins/template` 构建），清单在
  `local/cascade/Plugins.json`。
