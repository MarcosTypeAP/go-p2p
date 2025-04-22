package style

func Text(text string, styles ...Style) string {
	// Cmd does not support ANSI colors by default
	return text
}
