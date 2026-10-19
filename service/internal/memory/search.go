package memory

import (
	"strings"
	"unicode"
)

// maxTerms bounds the MATCH expression; a long message must not become a huge query.
const maxTerms = 12

// stopwords are dropped from recall queries: they match nearly every memory
// and drown the words that carry meaning.
var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`a about after again all also am an and any are as at be because been before being but by
		can could did do does doing for from had has have having he her here hers him his how i if in into is it its just me
		more most my no nor not now of off on once only or other our out over own same she should so some such than that the
		their them then there these they this those through to too under until up us very was we were what when where which
		while who whom why will with would you your yours hey hi hello please thanks thank okay ok yeah yes tell let lets
		get got gonna want need tonight today tomorrow
		don doesn didn isn aren wasn weren won wouldn couldn shouldn haven hasn hadn ll ve re`) {
		m[w] = true
	}
	return m
}()

// MatchQuery turns free text into a safe FTS5 MATCH expression: its words,
// lower-cased, stopwords removed, each quoted and joined with OR. Raw text is
// never passed to MATCH, where quotes, parentheses, "NEAR", "*" and column
// filters would be syntax. It returns "" when no usable word is left.
func MatchQuery(text string) string {
	var (
		terms []string
		seen  = map[string]bool{}
		word  []rune
	)
	flush := func() {
		if len(word) == 0 {
			return
		}
		w := strings.ToLower(string(word))
		word = word[:0]
		if len(terms) >= maxTerms || seen[w] || stopwords[w] || len([]rune(w)) < 2 {
			return
		}
		seen[w] = true
		terms = append(terms, `"`+w+`"`)
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
			word = append(word, r)
			continue
		}
		flush()
	}
	flush()
	return strings.Join(terms, " OR ")
}
