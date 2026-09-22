package notify

import "strings"

// escapeMarkup escapes the characters freedesktop notification servers treat as markup.
func escapeMarkup(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
