package tui

import (
	"encoding/base64"
	"os"
)

// yankToClipboard copies text to the system clipboard via OSC 52.
func yankToClipboard(text string) {
	b64 := base64.StdEncoding.EncodeToString([]byte(text))
	os.Stderr.WriteString("\033]52;c;" + b64 + "\a")
}
