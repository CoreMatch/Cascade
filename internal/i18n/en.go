package i18n

// enMessages holds every user-visible string the edge proxy sends through
// chat. The relay-era moderation/game-flow messages were deleted with the
// rest of the pivot leftovers (knowledge entry 0001): rooms, lobby state,
// and game protocol behavior belong to the backend.
var enMessages = map[string]string{
	// --- Chat rate limiting ---
	"msg.rate_limit": "You are sending messages too fast, please try again later.",

	// --- HA Authentication ---
	"msg.ha_not_configured":     "Authentication service is not configured.",
	"msg.already_authenticated": "You are already authenticated.",
	"msg.auth_failed_service":   "Authentication failed: service unavailable.",
	"msg.auth_failed_creds":     "Authentication failed: incorrect username or password.",
	"msg.auth_success":          "Authentication successful! Welcome, %s",
	"msg.login_usage":           "Usage: /login <email/username> <password>",

	// --- HA account claim ---
	"msg.claim_usage":         "Usage: /claim <new password> <email>",
	"msg.claim_already":       "Your account has already been claimed.",
	"msg.claim_not_found":     "Your account record was not found.",
	"msg.claim_pwd_too_short": "Password must be at least 6 characters.",
	"msg.claim_failed":        "Claim failed: %s",
	"msg.claim_bind_failed":   "Binding failed: %s",
	"msg.claim_success":       "Claim successful! Your game account is now linked to an HA account.",
	"msg.claim_email":         "Email: %s",
	"msg.claim_remember":      "Please remember your login credentials!",

	// --- Auth status ---
	"msg.whoami_authenticated": "Status: Authenticated",
	"msg.whoami_uuid":          "HA UUID: %s",
	"msg.whoami_guest":         "Status: Unauthenticated (Guest)",
	"msg.whoami_hint":          "Use /login <email> <password> to log in",

	// --- Language ---
	"msg.usage.lang":   "Usage: /lang <en|zh>",
	"msg.lang_changed": "Language changed to English.",

	// --- Lobby jump ---
	"msg.usage.goto":     "Usage: /goto <room code>  (see /rooms)",
	"msg.jump_disabled":  "Lobby jump is not configured on this proxy.",
	"msg.goto_not_found": "No room found with code: %s",
	"msg.goto_no_jump":   "Room %s has no jump entry configured.",
	"msg.goto_ok":        "Jumping to %s ...",
	"msg.rooms_empty":    "No rooms available.",
	"msg.rooms_header":   "Rooms (join with /goto <code>):",
	"msg.rooms_line":     "%s | %s | %d/%d players",
}
