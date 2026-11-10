package freetier

import "strings"

// Normalize maps a host provider, type, or filename prefix to a probed FreeTier
// vendor ID, or "".
func Normalize(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "workbuddyglobal", "workbuddy-global", "workbuddy_global":
		return "workbuddyglobal"
	case "workbuddycn", "workbuddy-cn", "workbuddy_cn", "codebuddy":
		return "workbuddycn"
	default:
		return fromFileName(value)
	}
}

// IsProvider reports whether value is a FreeTier vendor this package can probe.
func IsProvider(value string) bool {
	switch Normalize(value) {
	case "workbuddyglobal", "workbuddycn":
		return true
	default:
		return false
	}
}

func fromFileName(value string) string {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range []string{"workbuddyglobal", "workbuddycn"} {
		if strings.HasPrefix(lower, prefix+"-") || strings.HasPrefix(lower, prefix+".") || lower == prefix {
			return prefix
		}
	}
	return ""
}
