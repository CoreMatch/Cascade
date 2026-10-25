# MCAGS-RW

MCAGS-RW 是 Rusted Warfare 群组服务器的边缘反向代理。它接收客户端连接、按初始 `Preregister` 房间码选择后端、处理现有 HRPAuth 玩家认证，并在代理与独立后端之间转发原生游戏协议帧。

房间、大厅、对局和游戏协议行为由独立后端负责。本仓库不实现或维护生产后端。双方接口契约位于 [`HA-Contract/MCAGS-RW-Backend-v1.md`](HA-Contract/MCAGS-RW-Backend-v1.md)，配套 OpenAPI 位于 `HA-Contract/openapi/mcags-rw-backend-v1.yaml`。

## 配置与运行

程序首次启动会生成 `config.yaml`。启动前必须配置默认后端、mTLS 文件和代理签名密钥；不会自动猜测后端地址。最小后端配置示例：

```yaml
version: 2
game_listen_addr: ":5123"
http_listen_addr: ":8080"
default_backend_id: "backend-a"
backends:
  - id: "backend-a"
    control_url: "https://backend-a.example.net"
    game_address: "backend-a.example.net:5124"
    prefixes: ["GW"]
proxy_issuer: "mcags-rw"
proxy_key_id: "proxy-key-2026-01"
proxy_private_key_path: "/run/secrets/proxy-ed25519-key.pem"
backend_client_cert: "/run/secrets/proxy-client.crt"
backend_client_key: "/run/secrets/proxy-client.key"
backend_ca_pool: "/run/secrets/backend-ca.pem"
health_interval_sec: 5
room_snapshot_interval_sec: 5
pending_session_timeout_sec: 5
```

控制 API 和游戏 TCP 均使用 mTLS。证书、CA 和 Ed25519 PKCS#8 私钥通过受限文件挂载；客户端私钥和签名私钥权限不能允许 group/other 访问。首次试运行可执行：

```bash
go run ./cmd/rwgin
```

配置从 v1 自动迁移到 v2，现有玩家数据库和认证绑定保留。默认不限制连接数；可通过 `max_connections` 设置显式上限。

## 路由与接口

初始 `Preregister` 有房间码时，路由优先级为已知房间归属、最长匹配静态前缀、默认后端。没有房间码时使用默认后端；之后输入的房间码由已选后端处理，连接不会迁移。

- `GET /healthz`：进程存活状态。
- `GET /readyz`：默认后端就绪状态。
- `GET /api/backends`：后端健康、快照和房间归属冲突。
- `GET /api/rooms` 及原有 `/api/rooms` 管理路径：代理门面，操作转发给房间所属后端。
- 原有 `/api/auth/*` 与 `/internal/*` 接口继续由代理处理。

## 验证范围

仓内测试覆盖配置迁移、OpenAPI YAML 解析、路由目录、mTLS 控制与游戏传输、JWT 身份绑定和 RW 帧转发。它们不等同于真实客户端兼容验收。正式切换前仍需使用独立后端和真实 Rusted Warfare 客户端完成握手、入房及一局游戏，并在维护窗口切换。
