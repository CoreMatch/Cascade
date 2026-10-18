package i18n

import (
	"fmt"
	"strings"

	"golang.org/x/text/language"
)

// Supported locale constants.
const (
	LangEN = "en"
	LangZH = "zh"
)

var matcher = language.NewMatcher([]language.Tag{
	language.English,
	language.Chinese,
})

// NormalizeLang parses a user-supplied locale string and returns
// a canonical short code ("en" or "zh"). Falls back to "en".
func NormalizeLang(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	tag, _ := language.Parse(raw)
	base, _, _ := matcher.Match(tag)
	b, _ := base.Base()
	switch b.String() {
	case "zh":
		return LangZH
	default:
		return LangEN
	}
}

// messages holds all locale -> key -> template mappings.
var messages = map[string]map[string]string{
	LangEN: enMessages,
	LangZH: zhMessages,
}

// T returns the translated message for the given locale and key.
// If the key is not found in the requested locale, it falls back to English.
// Format arguments are applied via fmt.Sprintf if any are provided.
func T(locale, key string, args ...any) string {
	msg := lookup(locale, key)
	if len(args) > 0 {
		return fmt.Sprintf(msg, args...)
	}
	return msg
}

func lookup(locale, key string) string {
	if msgs, ok := messages[locale]; ok {
		if msg, ok := msgs[key]; ok {
			return msg
		}
	}
	// Fallback to English
	if msgs, ok := messages[LangEN]; ok {
		if msg, ok := msgs[key]; ok {
			return msg
		}
	}
	return key // Last resort: return the key itself
}