<div align="center">
  <h1>Cascade</h1>
  <p><strong>多个铁锈战争后端，一个公共入口。</strong></p>
  <p><a href="./README.md">English</a></p>
</div>

Cascade 是使用 Rusted Warfare 原生协议的边缘反向代理。它为运维者和集成者提供统一公共入口，连接多个独立游戏后端。

## 为什么选择 Cascade

- **统一前端入口。** 玩家连接 Cascade，各后端分别负责自己的房间与对局。
- **按上下文路由。** 精确房间、最长前缀与默认路由会在选择后端前检查健康状态。
- **传递可信身份。** 绑定 Armorial 的 JWT 校验支持身份感知访问，但不通用验证任意 JWT 签发方。
- **隐藏后端地址。** `/rooms` 与 `/goto` 通过公共重连引导玩家，不暴露后端地址。
- **明确选择传输方式。** 明文与 mTLS 模式适配不同的后端信任边界。
- **隔离扩展行为。** 进程隔离的签名插件可在代理进程之外增加边缘能力。

## 进一步了解

- [插件指南](docs/plugins/README.md) — 签名、进程隔离的扩展机制
- [Armorial 契约](https://github.com/CoreMatch/HA-Contract) — 权威代理与后端契约
- [Avalon](https://github.com/CoreMatch/Avalon) — 兼容的铁锈战争服务端运行时
- [Armorial Wiki](https://wiki.mcnb.dev/) — 生态部署与开发指南

## 使用边界

Cascade 不是游戏服务端，不提供自动故障转移或房间重新分配，也尚未经过生产环境验证。

## 许可证与来源

[AGPL-3.0](LICENSE) · 衍生自 [RW-HPS](https://github.com/RW-HPS/RW-HPS)，保留上游版权声明。
