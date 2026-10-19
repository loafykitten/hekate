package perform

import (
	"regexp"
	"strings"
	"unicode"
)

// Vocabulary lists the tags the character can perform. Anything else the model
// invents is dropped, never spoken.
type Vocabulary struct {
	Gestures    []string // {name}, from the asset manifest
	Expressions []string // [name], ElevenLabs voice tags
}

// DefaultExpressions are the ElevenLabs tags the persona rules allow.
var DefaultExpressions = []string{"laughs", "chuckles", "sighs", "whispers", "softly", "warmly"}

// Mark is a named tag at a rune offset.
type Mark struct {
	Name   string
	Offset int
}

// Tagged is a sentence with its tags separated out.
type Tagged struct {
	// Display is what the user reads: no tags, no markdown, tidy whitespace.
	Display string
	// TTSText is what is sent to the voice: Display plus the [expression] tags.
	TTSText string
	// Gestures hold rune offsets into Display.
	Gestures []Mark
	// Expressions hold rune offsets into TTSText (the position of the "[").
	Expressions []Mark
	// Dropped lists tags that were removed because they are not in the
	// vocabulary, as written (e.g. "{dance}"), so callers can log them.
	Dropped []string
}

var (
	curlyTag  = regexp.MustCompile(`\{[^{}\[\]\n]{0,40}\}`)
	squareTag = regexp.MustCompile(`\[[^{}\[\]\n]{0,40}\]`)
	mdLink    = regexp.MustCompile(`\[([^\[\]]*)\]\([^)]*\)`)
	listHead  = regexp.MustCompile(`(?m)^[ \t]*(?:[-*•]|\d{1,2}[.)])[ \t]+`)
	underEdge = regexp.MustCompile(`(^|[\s(])_+|_+($|[\s).,!?;:])`)
)

// sentinel runes stand in for tags while markdown is stripped, so that the
// offsets can be computed on the final text. They come from a private-use
// block that never appears in model output.
const sentinelBase rune = 0xE000

// Parse separates tags from text.
func Parse(sentence string, v Vocabulary) Tagged {
	type tag struct {
		name    string
		gesture bool
	}
	var (
		tags    []tag
		dropped []string
		next    = sentinelBase
	)
	add := func(name string, gesture bool) string {
		tags = append(tags, tag{name, gesture})
		next++
		return string(next - 1)
	}
	work := mdLink.ReplaceAllString(sentence, "$1")
	work = curlyTag.ReplaceAllStringFunc(work, func(m string) string {
		name := strings.ToLower(strings.TrimSpace(m[1 : len(m)-1]))
		if !contains(v.Gestures, name) {
			dropped = append(dropped, m)
			return ""
		}
		return add(name, true)
	})
	work = squareTag.ReplaceAllStringFunc(work, func(m string) string {
		name := strings.ToLower(strings.TrimSpace(m[1 : len(m)-1]))
		if !contains(v.Expressions, name) {
			dropped = append(dropped, m)
			return ""
		}
		return add(name, false)
	})
	work = stripMarkdown(work)

	var display, tts []rune
	var t Tagged
	t.Dropped = dropped
	space := func(buf []rune) bool { return len(buf) == 0 || unicode.IsSpace(buf[len(buf)-1]) }
	for _, r := range work {
		switch {
		case r >= sentinelBase && r < next:
			tg := tags[r-sentinelBase]
			if tg.gesture {
				t.Gestures = append(t.Gestures, Mark{tg.name, len(display)})
				continue
			}
			t.Expressions = append(t.Expressions, Mark{tg.name, len(tts)})
			tts = append(tts, []rune("["+tg.name+"]")...)
		case unicode.IsSpace(r):
			if !space(display) {
				display = append(display, ' ')
			}
			if !space(tts) {
				tts = append(tts, ' ')
			}
		case r == '{' || r == '}' || r == '[' || r == ']':
			// a stray bracket is never worth speaking
		default:
			display = append(display, r)
			tts = append(tts, r)
		}
	}

	// Trim trailing space and keep offsets inside the text.
	for len(display) > 0 && display[len(display)-1] == ' ' {
		display = display[:len(display)-1]
	}
	for len(tts) > 0 && tts[len(tts)-1] == ' ' {
		tts = tts[:len(tts)-1]
	}
	t.Display, t.TTSText = string(display), string(tts)
	for i := range t.Gestures {
		t.Gestures[i].Offset = min(t.Gestures[i].Offset, len(display))
	}
	return t
}

func stripMarkdown(s string) string {
	s = listHead.ReplaceAllString(s, "")
	s = strings.NewReplacer("*", "", "`", "", "#", "", "~~", "").Replace(s)
	s = underEdge.ReplaceAllString(s, "$1$2")
	return strings.ReplaceAll(s, "_", " ")
}

func contains(list []string, name string) bool {
	for _, s := range list {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}
