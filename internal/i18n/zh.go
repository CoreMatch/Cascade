package i18n

// zhMessages 与 enMessages 一一对应；键集合必须保持一致。
var zhMessages = map[string]string{
	// --- 聊天限流 ---
	"msg.rate_limit": "消息发送过快，请稍后再试。",

	// --- HA 认证 ---
	"msg.ha_not_configured":     "认证服务未配置。",
	"msg.already_authenticated": "你已经认证过了。",
	"msg.auth_failed_service":   "认证失败: 服务不可用。",
	"msg.auth_failed_creds":     "认证失败: 用户名或密码错误。",
	"msg.auth_success":          "认证成功！欢迎, %s",
	"msg.login_usage":           "用法: /login <邮箱/用户名> <密码>",

	// --- HA 账号认领 ---
	"msg.claim_usage":         "用法: /claim <新密码> <邮箱>",
	"msg.claim_already":       "你的账号已经被认领过了。",
	"msg.claim_not_found":     "未找到你的账号记录。",
	"msg.claim_pwd_too_short": "密码长度至少 6 位。",
	"msg.claim_failed":        "认领失败: %s",
	"msg.claim_bind_failed":   "绑定失败: %s",
	"msg.claim_success":       "认领成功！你的游戏账号已关联到 HA 账号。",
	"msg.claim_email":         "邮箱: %s",
	"msg.claim_remember":      "请记住你的登录信息！",

	// --- 认证状态 ---
	"msg.whoami_authenticated": "状态: 已认证",
	"msg.whoami_uuid":          "HA UUID: %s",
	"msg.whoami_guest":         "状态: 未认证 (游客)",
	"msg.whoami_hint":          "使用 /login <邮箱> <密码> 登录",

	// --- 语言 ---
	"msg.usage.lang":   "用法: /lang <en|zh>",
	"msg.lang_changed": "语言已切换为中文。",

	// --- 大厅跳转 ---
	"msg.usage.goto":     "用法: /goto <房间码>  (用 /rooms 查看)",
	"msg.jump_disabled":  "本代理未配置大厅跳转。",
	"msg.goto_not_found": "未找到房间码为 %s 的房间",
	"msg.goto_no_jump":   "房间 %s 未配置跳转入口。",
	"msg.goto_ok":        "正在跳转到 %s ...",
	"msg.rooms_empty":    "当前没有可用房间。",
	"msg.rooms_header":   "房间列表 (用 /goto <房间码> 加入):",
	"msg.rooms_line":     "%s | %s | %d/%d 玩家",

	// --- MCA 对局路由 (按成局分配转向, 复用 /goto 的跳转机制) ---
	"msg.route_ok":             "匹配成功！正在送你进入对局服务器...",
	"msg.route_no_room":        "当前对局暂无可用房间，你留在大厅，请稍后再试。",
	"msg.route_no_target":      "匹配成功，但未提供游戏服务器地址，请联系管理员。",
	"msg.route_unknown_target": "匹配成功，但代理未配置对应的游戏服务器，请联系管理员。",
	"msg.route_target_unready": "无法进入对局：目标服务器未就绪，请稍后重试。",
	"msg.route_no_jump":        "无法进入对局：目标服务器未配置跳转入口，请联系管理员。",
	"msg.route_denied":         "无法进入对局：%s",
}
