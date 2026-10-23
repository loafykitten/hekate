// Package persona embeds the prompt text that defines Renamon. The files hold
// {{tamer}} and {{gestures}} placeholders that the agent fills in per turn.
package persona

import (
	_ "embed"
	"strings"
)

// Renamon is who she is and how she talks.
//
//go:embed renamon.md
var Renamon string

// Rules is how she works with tools and performs tags.
//
//go:embed rules.md
var Rules string

// Thread is how she writes and works in a thread: a text conversation of its
// own for longer work.
//
//go:embed thread.md
var Thread string

// ThreadLab is her lab, for threads when the lab is set up. It holds a {{lab}}
// placeholder: the name the owner's devices reach it by.
//
//go:embed thread_lab.md
var ThreadLab string

// Section returns the "## heading" section of doc, heading included, or "".
func Section(doc, heading string) string {
	marker := "## " + heading + "\n"
	start := 0
	if !strings.HasPrefix(doc, marker) {
		i := strings.Index(doc, "\n"+marker)
		if i < 0 {
			return ""
		}
		start = i + 1
	}
	body := doc[start+len(marker):]
	if end := strings.Index(body, "\n## "); end >= 0 {
		body = body[:end]
	}
	return strings.TrimSpace(marker + body)
}
