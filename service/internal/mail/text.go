package mail

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"github.com/emersion/go-message/mail"

	// Registers decoders for the legacy charsets mail still uses.
	_ "github.com/emersion/go-message/charset"

	"github.com/loafykitten/hekate/service/internal/web"
)

// maxRawBody is how much of a message is fetched to read it. Bigger messages
// are mostly attachments; the text parts come first.
const maxRawBody = 512 << 10

// bodyText extracts readable text from a raw RFC 822 message: the text/plain
// part when there is one, else the HTML part flattened. Attachments are
// skipped. A truncated message yields whatever parsed before the cut, so an
// error is only returned when nothing could be read.
func bodyText(raw []byte) (string, error) {
	mr, err := mail.CreateReader(bytes.NewReader(raw))
	if err != nil && mr == nil {
		return "", err //nolint:wrapcheck // the caller adds context
	}
	var plain, htmlText string
	for {
		p, err := mr.NextPart()
		if err != nil {
			break // io.EOF, or a truncated/garbled tail: use what we have
		}
		h, ok := p.Header.(*mail.InlineHeader)
		if !ok {
			continue // attachment
		}
		ct, _, _ := h.ContentType()
		switch strings.ToLower(ct) {
		case "text/plain":
			if plain == "" {
				plain = readText(p.Body)
			}
		case "text/html":
			if htmlText == "" {
				htmlText = readText(p.Body)
				if t, err := web.HTMLText(strings.NewReader(htmlText)); err == nil {
					htmlText = t
				}
			}
		}
	}
	text := strings.TrimSpace(plain)
	if len(strings.Fields(text)) < 3 && htmlText != "" {
		text = strings.TrimSpace(htmlText)
	}
	if text == "" && plain == "" && htmlText == "" {
		return "", errors.New("the message has no readable text")
	}
	return tidy(text), nil
}

func readText(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, maxRawBody))
	return strings.ToValidUTF8(string(b), "")
}

// tidy squeezes whitespace: single spaces within lines, at most one blank line
// between paragraphs.
func tidy(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, l := range lines {
		l = strings.Join(strings.Fields(l), " ")
		if l == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
