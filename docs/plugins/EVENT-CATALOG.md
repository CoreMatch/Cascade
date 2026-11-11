# 事件目录（cascade-plugin/v1）

事件名是稳定标识符，用于清单订阅、协商与端点路径
（`POST {base}/v1/events/{name}`）。v1 全部事件只有版本 1。

| 事件 | 触发时刻 | 档位 | 可否决 |
|---|---|---|---|
| `player.registered` | 玩家注册 + 后端会话身份绑定成功后 | 观察 | 否 |
| `player.route` | MCA 成局决策产生、178 跳转包发出前 | 双模 | 是（须授权+声明） |
| `player.routed` | 178 跳转包成功写入客户端连接后 | 观察 | 否 |

## 公共信封

所有事件载荷同形（与 Avalon 信封一致）：

| 字段 | 类型 | 说明 |
|---|---|---|
| `eventId` | string | `evt_` + 26 字符 ULID；生成一次即固定 |
| `event` | string | 事件名 |
| `eventVersion` | int | 恒为 1 |
| `occurredAt` | string | RFC3339Nano（UTC） |
| `proxy` | object | 宿主段，见下 |
| `player` | object | 玩家段，见下 |

`proxy` 段：

| 字段 | 说明 |
|---|---|
| `proxyId` | 配置 `proxy_issuer`（运营可辨认的实例标识） |
| `publicHost` | `public_game_host`（跳转目标主机，可省略） |
| `hostVersion` | 插件协议宿主版本（可观测用） |

`player` 段（插件是密钥鉴权的运营可信基础设施，与 Avalon 插件事件
携带玩家档案同一信任级别）：

| 字段 | 说明 |
|---|---|
| `playerId` | 游戏侧玩家 ID |
| `name` | 玩家名 |
| `authenticated` | 是否已绑定 HRPAuth 账号 |
| `haUserUuid` | 已绑定时的 HRPAuth 账号 UUID（游客省略） |

## player.registered

附加字段：

| 字段 | 说明 |
|---|---|
| `backend.backendId` | 绑定完成时所在后端（通常是大厅） |

## player.route（双模）

附加字段：

| 字段 | 说明 |
|---|---|
| `backend.backendId` | 当前所在后端 |
| `decision.source` | 决策来源，当前恒为 `mca` |
| `decision.backendId` | 决策要送往的后端 ID |
| `decision.joinTarget` | MCA 成局携带的子服 `host:port`（可省略） |
| `decision.groupId` / `matchType` / `teamId` | 成局信息（日志用） |

**裁决应答**（仅参与否决咨询的插件需要）：

```json
{"allowed": true}
{"allowed": false, "reason": "可读的否决理由（必填）"}
```

- 任一授权插件否决即否决：玩家留在大厅，收到本地化否决消息（中文为
  `无法进入对局：<理由>`；理由净化：去控制符、截断 200 rune、带插件名前缀）。
- 放行时全部订阅者（含参与否决者）收到同一 `eventId` 的观察副本。
- 否决时转向不发生，无观察副本。
- 未授权/未在 manifest 声明 veto 的订阅者按观察档收到事件，不参与咨询。

## player.routed

附加字段：

| 字段 | 说明 |
|---|---|
| `backend.backendId` | 目标后端 ID |
| `target` | 178 跳转目标（代理自己的 `publicHost:jumpPort`，非后端真实地址） |
