# 鉴权与协商（cascade-plugin/v1）

## 双向 HMAC-SHA256 签名

请求与应答**都**携带两个头：

| 头 | 值 |
|---|---|
| `X-Cascade-Timestamp` | unix 秒（十进制字符串） |
| `X-Cascade-Signature` | `v1=` + hex(HMAC-SHA256(secret, `"<unix秒>.<原始正文>"`)) |

- 签名覆盖时间戳与原始正文，防两者被篡改；`GET /v1/manifest` 的正文按
  空串参与签名。
- 时间戳偏移容忍 ±300 秒。
- Cascade 用 `secrets[0]`（主密钥）签名出站请求；验证应答时逐个尝试
  全部密钥（列表语义支持轮换重叠期）。插件侧反之：用自己持有的密钥
  验证请求、签名应答。
- 密钥为至少 16 字节的 hex（`Plugins.json` 与插件 `-secret` 一致）。
- 验签失败不是错误而是攻击面：插件必须回 401；Cascade 把无签名/验签
  失败的应答按畸形应答处理（见 FAILURE-MODES）。
- HTTP 客户端不跟随 3xx：重定向后的请求不再携带原签名语义，一律按
  失败处理。

## manifest 协商

- 端点：`GET {base}/v1/manifest`（签名请求 + 签名应答）。
- 应答正文上限 64KB；应答必须是合法 JSON 且：

```json
{
  "schema": "cascade-plugin/manifest",
  "protocol": "cascade-plugin/v1",
  "id": "<插件自报>",
  "version": "<插件自报>",
  "events": { "<事件名>": {"versions": [1], "veto": false} },
  "timeoutMs": 0,
  "maxConcurrency": 1
}
```

- `schema`/`protocol` 必须精确匹配（Cascade 插件不得讲 Avalon 方言，
  反之亦然）；`id`/`version` 非空。
- 协商结果 = 逐事件版本交集（v1 只有版本 1）；`veto: true` 仅在
  双模事件（`player.route`）上有意义，且还须运营在 `vetoEvents`
  授权才生效（**双闸门，默认拒绝**）。
- `timeoutMs` 是插件自报处理预算：Cascade 用自己的上限取 min 封顶。
- `maxConcurrency` 为保留字段：Cascade 恒按单 worker 对待（保序）。

## 协商时机

- 启动时并行首轮协商（每插件独立超时 = `admissionTimeoutMs`）；
  失败只记日志，启动不被不可达插件拖死。
- 未就绪插件由各自 worker 后台重试，退避 5s → 5min。
- 就绪后发生连接级失败（拒连/DNS/进程死）→ 标记 unready，回到协商
  路径；超时**不算**连接级失败（走投递失败语义）。
- 协商成功时丢弃积压中版本不匹配的事件并计数。
