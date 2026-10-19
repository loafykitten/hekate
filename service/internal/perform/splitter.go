// Package perform turns the model's streamed text into performable segments:
// it splits sentences, strips and records {gesture} and [expression] tags, and
// maps tag offsets and words onto the audio timeline. It is pure: no I/O.
package perform

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	minSentenceRunes = 12  // shorter fragments are merged into the next sentence
	maxSentenceRunes = 220 // longer ones are cut at a comma, semicolon or dash
	// The first sentence of a reply is cut sooner: speech synthesis time grows with
	// length, and it decides how long the user waits to hear anything.
	firstSentenceRunes = 60
	maxTagBytes        = 80 // an unclosed tag opener is treated as plain text after this
)

// abbreviations never end a sentence when followed by a period. Entries are
// lower-case and exclude the final period.
var abbreviations = map[string]bool{
	"mr": true, "mrs": true, "ms": true, "dr": true, "st": true, "jr": true, "sr": true,
	"prof": true, "vs": true, "etc": true, "e.g": true, "i.e": true, "a.m": true, "p.m": true,
	"approx": true, "inc": true, "ltd": true, "mt": true,
}

// Splitter cuts streamed text into complete sentences. It is not safe for
// concurrent use.
type Splitter struct {
	buf     string
	carry   string // a short sentence waiting to be merged into the next one
	emitted int    // sentences returned so far
}

// Push adds streamed text and returns the sentences that are now complete.
func (s *Splitter) Push(delta string) []string {
	s.buf += delta
	return s.drain(false)
}

// Flush returns everything still buffered, including a trailing fragment.
func (s *Splitter) Flush() []string {
	out := s.drain(true)
	rest := strings.TrimSpace(s.buf)
	s.buf = ""
	if rest != "" || s.carry != "" {
		out = append(out, s.merge(rest))
		s.carry = ""
	}
	return out
}

func (s *Splitter) drain(final bool) []string {
	var out []string
	for {
		limit := maxSentenceRunes
		if s.emitted == 0 {
			limit = firstSentenceRunes
		}
		cut := findCut(s.buf, final, limit)
		if cut <= 0 {
			return out
		}
		sent := strings.TrimSpace(s.buf[:cut])
		s.buf = s.buf[cut:]
		if sent == "" {
			continue
		}
		sent = s.merge(sent)
		if !final && visibleRunes(sent) < minSentenceRunes {
			s.carry = sent
			continue
		}
		s.carry = ""
		s.emitted++
		out = append(out, sent)
	}
}

// merge prepends a carried short fragment.
func (s *Splitter) merge(sent string) string {
	switch {
	case s.carry == "":
		return sent
	case sent == "":
		return s.carry
	}
	return s.carry + " " + sent
}

// visibleRunes counts runes outside closed {gesture} tags, which are never spoken.
func visibleRunes(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == '{' {
			if end := strings.IndexByte(s[i:], '}'); end > 0 && end <= maxTagBytes {
				i += end + 1
				continue
			}
		}
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
		n++
	}
	return n
}

// findCut returns the byte offset just after the first complete sentence in b,
// or -1 if more text is needed. With final set, a terminator at the very end
// counts too. Past maxRunes, the sentence is cut at its last soft break.
func findCut(b string, final bool, maxRunes int) int {
	var (
		openAt   = -1 // byte index of an open { or [, or -1
		closer   rune
		runes    int
		lastSoft = -1 // byte index of the space after the last comma/semicolon/dash
	)
	for i := 0; i < len(b); {
		r, w := utf8.DecodeRuneInString(b[i:])
		switch {
		case openAt >= 0:
			if r == closer {
				openAt = -1
			} else if i-openAt > maxTagBytes {
				openAt = -1 // never closed: it was ordinary text
				continue
			}
			i += w
			continue
		case r == '{':
			openAt, closer = i, '}'
			i += w
			continue
		case r == '[':
			openAt, closer = i, ']'
			i += w
			continue
		case r == '\n':
			return i + w
		}

		if isTerminator(r) {
			j := i
			for j < len(b) {
				c, cw := utf8.DecodeRuneInString(b[j:])
				if !isTerminator(c) {
					break
				}
				j += cw
			}
			end := j
			for end < len(b) { // closing quotes and parentheses belong to the sentence
				c, cw := utf8.DecodeRuneInString(b[end:])
				if !strings.ContainsRune(`"'”’)`, c) {
					break
				}
				end += cw
			}
			switch {
			case end >= len(b):
				if final {
					return end
				}
				return -1 // the next character decides
			case nextIsSpace(b[end:]):
				if b[i:j] != "." || !notSentenceEnd(b[:i]) {
					return end
				}
			}
			runes += utf8.RuneCountInString(b[i:end])
			i = end
			continue
		}

		runes++
		if unicode.IsSpace(r) {
			// a soft break is a comma, semicolon or dash right before a space
			if prev, _ := utf8.DecodeLastRuneInString(b[:i]); strings.ContainsRune(",;—–-", prev) {
				lastSoft = i
			}
		}
		if runes > maxRunes && lastSoft > 0 {
			return lastSoft
		}
		i += w
	}
	return -1
}

func isTerminator(r rune) bool { return r == '.' || r == '!' || r == '?' || r == '…' }

func nextIsSpace(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsSpace(r)
}

func lastToken(s string) string {
	i := strings.LastIndexFunc(s, unicode.IsSpace)
	return s[i+1:]
}

// notSentenceEnd reports whether a period after the last token of prefix is
// part of an abbreviation, initial or list number rather than a sentence end.
func notSentenceEnd(prefix string) bool {
	tok := strings.ToLower(strings.TrimLeft(lastToken(prefix), `"'“‘(`))
	if tok == "" {
		return false
	}
	if abbreviations[tok] {
		return true
	}
	if utf8.RuneCountInString(tok) == 1 {
		r, _ := utf8.DecodeRuneInString(tok)
		if unicode.IsLetter(r) && r != 'i' && r != 'a' { // "J. K. Rowling", not "so do I."
			return true
		}
	}
	// "2." opening a line is a list marker; mid-sentence it can end a sentence.
	return strings.Trim(tok, "0123456789") == "" && strings.TrimSpace(prefix[:len(prefix)-len(lastToken(prefix))]) == ""
}
