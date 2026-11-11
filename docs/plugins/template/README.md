# Cascade 插件模板（route-watcher）

cascade-plugin/v1 契约的参考实现，可整体拷出独立编译（仅 Go 标准库）。
机制与 Avalon `docs/plugins/template` 同构：插件是独立 HTTP 服务，
Cascade 是调用方。

## 运行

```bash
go build -o route-watcher .
./route-watcher -addr 127.0.0.1:9801 -secret <32+位hex，与 Plugins.json 一致>
```

Cascade 工作目录的 `Plugins.json` 登记：

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

Cascade 启动后来拉 `/v1/manifest` 完成握手（日志 `[plugin] route-watcher
ready: ...`），随后按订阅投递事件。`vetoEvents` 缺省时插件只按观察档
收 `player.route`，不能否决。

## 演练开关

| 开关 | 效果 | 验证的宿主语义 |
|---|---|---|
| `-deny` | 否决全部 `player.route` | 否决拦截：玩家留大厅并收到理由 |
| `-hang-ms 5000` | 裁决前睡眠 | 超时 fail-open：Cascade 按放行处理并计数 |
| `-crash-after 1` | 处理 1 个事件后退出 | 崩溃隔离：主链路不受影响，后台重协商 |
