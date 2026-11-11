# 快速上手：写一个 Cascade 插件

以参考实现 `template/`（route-watcher）为例，从零到一个真实收事件的
进程外插件。全程不需要改 Cascade 源码。

## 1. 准备密钥

密钥是插件与 Cascade 共享的 hex 串（≥16 字节 = 32 个 hex 字符）：

```bash
openssl rand -hex 32
```

## 2. 起插件进程

```bash
cd docs/plugins/template
go run . -addr 127.0.0.1:9801 -secret <上一步的 hex>
```

看到 `route-watcher listening on 127.0.0.1:9801` 即就绪。插件只是监听，
**不需要主动连接 Cascade**——方向是 Cascade 调用插件。

## 3. 登记清单

在 Cascade 工作目录（`config.yaml` 所在目录）创建/编辑 `Plugins.json`：

```json
{
  "version": 1,
  "entries": [
    {
      "name": "route-watcher",
      "baseUrl": "http://127.0.0.1:9801",
      "secrets": ["<与 -secret 相同的 hex>"],
      "enabled": true,
      "vetoEvents": ["player.route"]
    }
  ]
}
```

- `name` 唯一，用于日志与状态；
- `secrets` 第一个是签名主密钥，列表支持轮换重叠期；
- `vetoEvents` 是否决授权，**默认拒绝**：不写则插件只能观察
  `player.route`，不能否决。

## 4. 启动/重启 Cascade

```
[plugin] route-watcher ready: id=route-watcher version=0.1.0 events=[player.registered, player.route, player.routed]
```

出现该行即握手成功。若出现 `manifest negotiation failed at boot`：
插件没起、地址/密钥不一致、或 manifest 字段不符契约——按
AUTH-AND-NEGOTIATION.md 核对；Cascade 会后台退避重试，不影响主链路。

## 5. 验证事件真的到了

任一玩家完成注册（真实客户端或探针）后，插件日志应出现：

```
[player.registered] id=evt_... player=<名字>(<玩家ID>) backend=<后端> authenticated=<bool>
```

若配置了 MCA 对局路由（`config.yaml` 的 `mca` 块）且玩家持有有效成局，
还会依次出现 `[player.route]`（否决咨询 + 放行后的观察副本，同一
`eventId` 两条）与 `[player.routed]`。

## 6. 故障演练（可选）

模板自带演练开关，用于验证宿主的失败隔离：

```bash
go run . -addr 127.0.0.1:9801 -secret <hex> -deny          # 否决全部转向
go run . -addr 127.0.0.1:9801 -secret <hex> -hang-ms 5000  # 超时 fail-open
go run . -addr 127.0.0.1:9801 -secret <hex> -crash-after 1  # 崩溃隔离
```

预期行为逐条见 FAILURE-MODES.md。

## 常见错误速查

| 现象 | 原因 |
|---|---|
| `401` 拒绝 | 密钥不一致 / 时间戳偏移超 ±300s |
| 握手失败 `manifest schema/protocol` | 插件讲了别的方言（如 avalon-plugin/v1） |
| 事件没到 | 未在 `Handle` 订阅（manifest 未声明）；或插件未就绪 |
| 否决不生效 | 未在 `vetoEvents` 授权，或裁决 `allowed:false` 没给 `reason`（无理由 = 畸形应答 = fail-open） |
