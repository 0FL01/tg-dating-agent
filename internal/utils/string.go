package utils

func Truncate(s string, max int) string {
	if max < 0 {
		max = 0
	}

	runes := []rune(s)
	if len(runes) <= max {
		return s
	}

	return string(runes[:max]) + "..."
}
