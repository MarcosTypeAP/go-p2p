package style

import "strings"

func Text(text string, styles ...Style) string {
	return strings.Join(styles, "") + text + Reset
}
