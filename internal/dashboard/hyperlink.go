package dashboard

import "fmt"

// Hyperlink wraps text in an OSC 8 escape sequence pointing at url. Modern
// terminals render this as a clickable link; terminals that do not support it
// strip the sequence cleanly and show only the text. We emit unconditionally
// — detection across the long tail of terminals is unreliable.
func Hyperlink(url, text string) string {
	if url == "" {
		return text
	}
	return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", url, text)
}
