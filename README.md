# Cascade

Cascade 是 Rusted Warfare 群组服务器的边缘反向代理。它接收客户端连接、按初始 `Preregister` 房间码选择后端、处理现有 HRPAuth 玩家认证，并在代理与独立后端之间转发原生游戏协议帧。

房间、大厅、对局和游戏协议行为由独立后端负责。本仓库不实现或维护生产后端。双方接口契约的权威源位于 HA-Contract 仓库：[`docs/dev/Cascade/Cascade-Backend-v1.md`](https://github.com/CoreMatch/HA-Contract/blob/main/docs/dev/Cascade/Cascade-Backend-v1.md)，配套 OpenAPI 为 [`docs/api/openapi/cascade-backend-v1.yaml`](https://github.com/CoreMatch/HA-Contract/blob/main/docs/api/openapi/cascade-backend-v1.yaml)。

## 配置与运行

程序首次启动会生成 `config.yaml`。启动前必须配置默认后端和代理签名密钥；不会自动猜测后端地址。全部配置项及注释见 [`config.example.yaml`](config.example.yaml)。最小后端配置示例（后端按 `open` 信任档运行，明文传输）：

```yaml
version: 2
game_listen_addr: ":5123"
http_listen_addr: ":8080"
default_backend_id: "backend-a"
backends:
  - id: "backend-a"
    control_url: "http://backend-a.example.net:5125"
    game_address: "backend-a.example.net:5124"
    prefixes: ["GW"]
    trust_mode: "open"
proxy_issuer: "cascade"
proxy_key_id: "proxy-key-2026-01"
proxy_private_key_path: "/run/secrets/proxy-ed25519-key.pem"
health_interval_sec: 5
room_snapshot_interval_sec: 5
pending_session_timeout_sec: 5
```

对每个后端，传输方式按契约 v1.2 从后端声明的信任档推导：每后端 `trust_mode` 声明其信任档（`open`/`ip`/`pinned`），`tls_mode`（`auto`/`mtls`/`plain`，默认 `auto`）决定是否覆盖。`auto` 时 `open`/`ip` 走明文、`pinned` 走 mTLS，声明缺失或无法解析时按最安全可用档走 mTLS（失败即关闭，不静默降级明文）。明文传输没有传输机密性与对端认证，仅适用于受信任网络。走 mTLS 的后端要求 `control_url` 为 `https` 并配置下方三件证书材料；走明文的后端要求 `control_url` 为 `http`，不需要任何证书材料。证书、CA 和 Ed25519 PKCS#8 私钥通过受限文件挂载；客户端私钥和签名私钥权限不能允许 group/other 访问。首次试运行可执行：

```bash
go run ./cmd/cascade
```

配置从 v1 自动迁移到 v2，玩家表结构与认证绑定保留。默认不限制连接数；可通过 `max_connections` 设置显式上限。

相对路径先按工作目录、再按可执行文件所在目录解析：`config.yaml` 与 SQLite `db_path` 已存在时从任一位置读取（新文件创建在工作目录），数据库迁移目录同理。独立二进制部署必须随附 `internal/database/migrations` 目录，否则启动时显式报错退出，不会静默跳过建表。

> **从 MCAGS-RW 升级**：默认 SQLite 路径已改为 `data/cascade.db`，请将旧库 `data/rwgin.db` 迁移到新路径（或显式设置 `db_path`）；同时 `proxy_issuer`、`hrpauth_service_name` 与后端 preamble magic 均已变更，需与后端同步更新并重新签发令牌。

## 路由与接口

初始 `Preregister` 有房间码时，路由优先级为已知房间归属、最长匹配静态前缀、默认后端。没有房间码时使用默认后端；之后输入的房间码由已选后端处理，连接不会迁移。

- `GET /healthz`：进程存活状态。
- `GET /readyz`：默认后端就绪状态。
- `GET /api/backends`：后端健康、快照、房间归属冲突和每个后端解析出的传输方式（`mtls`/`plain`）。
- `GET /api/rooms` 及原有 `/api/rooms` 管理路径：代理门面，操作转发给房间所属后端。
- 原有 `/api/auth/*` 与 `/internal/*` 接口继续由代理处理。

管理面的全部错误响应使用统一信封 `{"success":false,"code":...,"message":...,"meta":{"request_id":...}}`：`code` 为 HA-Contract `docs/api/error-codes.md` 登记的 `lower_snake` 稳定码（如 `cascade_room_conflict`、`cascade_binding_not_found`），后端契约的 `UPPER_SNAKE` 稳定码（`FORBIDDEN`、`SESSION_NOT_FOUND`、`ROOM_CONFLICT` 等）连同状态码与消息原样透传。成功响应形状不变。

## 大厅跳转的运维口径（/goto 与 /rooms）

`/goto <房间码>` 与 `/rooms` 是**代理侧**的游戏内聊天命令，由代理直接应答（`internal/proxy/session.go::commandGoto`、`internal/proxy/session.go::commandRooms`）：`/rooms` 列出健康后端当前拥有的房间，`/goto` 把玩家导向目标房间所在后端的子服大厅。

- **跳转目标恒为代理的公网口**：重连包（`PACKET_RECONNECT_TO`）写入 `public_game_host` + 目标后端的 `jump_port`（`internal/config/config.go::PublicGameHost`、`internal/config/config.go::JumpPort`），**从不下发后端真实地址**——后端保持私有，代理是唯一公网入口。`jump_port` 是代理为该后端额外监听的公开端口，落点连接无论携带什么房间码都直达该后端。
- **后端游戏口只需对代理可达**，不需要对玩家开放；防火墙按"只有代理的 `game_listen_addr` 与跳转端口对外"来配。
- `public_game_host` 留空即整体关闭大厅跳转（代理拒绝 `/goto`）；它必须是 vanilla 客户端可公网解析/到达的主机名或 IP。

设计理由与端口方案的取舍见 `knowledge/0007-lobby-jump-port-routing.md`。

## 验证范围

仓内测试覆盖配置迁移、OpenAPI YAML 解析、路由目录、按信任档推导的明文与 mTLS 控制/游戏传输、JWT 身份绑定和 RW 帧转发。它们不等同于真实客户端兼容验收。正式切换前仍需使用独立后端和真实 Rusted Warfare 客户端完成握手、入房及一局游戏，并在维护窗口切换。
