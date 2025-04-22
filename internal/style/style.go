package style

type Style = string

const (
	Reset Style = "\033[0m"

	Bold      Style = "\033[1m"
	Underline Style = "\033[4m"

	White  Style = "\033[39m"
	Blue   Style = "\033[34m"
	Yellow Style = "\033[33m"
	Green  Style = "\033[32m"
	Red    Style = "\033[31m"
)
