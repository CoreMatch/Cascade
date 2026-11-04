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
}
