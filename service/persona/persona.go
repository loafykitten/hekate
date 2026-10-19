// Package persona embeds the prompt text that defines Renamon. The files hold
// {{tamer}} and {{gestures}} placeholders that the agent fills in per turn.
package persona

import _ "embed"

// Renamon is who she is and how she talks.
//
//go:embed renamon.md
var Renamon string

// Rules is how she works with tools and performs tags.
//
//go:embed rules.md
var Rules string
