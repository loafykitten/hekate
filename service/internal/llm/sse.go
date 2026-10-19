package llm

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// Limits that keep a misbehaving provider from exhausting memory.
const (
	maxStreamBytes = 16 << 20  // per stream
	maxLineBytes   = 256 << 10 // per SSE line
)

var (
	errStreamTooLarge = errors.New("stream exceeds 16 MiB")
	errLineTooLong    = errors.New("SSE line exceeds 256 KiB")
)

// limitedReader fails (rather than silently truncating) once n bytes were read.
type limitedReader struct {
	r io.Reader
	n int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errStreamTooLarge
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err //nolint:wrapcheck // pass-through reader
}

// sseReader parses text/event-stream. Lines end in LF, CRLF or a lone CR.
type sseReader struct {
	br        *bufio.Reader
	onComment func(string)
}

func newSSEReader(r io.Reader, onComment func(string)) *sseReader {
	return &sseReader{
		br:        bufio.NewReaderSize(&limitedReader{r: r, n: maxStreamBytes}, 32<<10),
		onComment: onComment,
	}
}

// readLine returns the next line without its terminator. At end of input it
// returns what is buffered (if anything) and then io.EOF.
func (s *sseReader) readLine() (string, error) {
	var sb strings.Builder
	for {
		b, err := s.br.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) && sb.Len() > 0 {
				return sb.String(), nil
			}
			return "", err //nolint:wrapcheck // callers inspect io.EOF
		}
		switch b {
		case '\n':
			return sb.String(), nil
		case '\r':
			// CRLF counts as one terminator.
			if next, err := s.br.Peek(1); err == nil && next[0] == '\n' {
				_, _ = s.br.Discard(1)
			}
			return sb.String(), nil
		}
		if sb.Len() >= maxLineBytes {
			return "", errLineTooLong
		}
		sb.WriteByte(b)
	}
}

// next returns the data of the next event (multiple data lines joined by
// "\n") and its event name. io.EOF means the input ended between events.
func (s *sseReader) next() (data, event string, err error) {
	var lines []string
	hasData := false
	for {
		line, err := s.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) && hasData {
				// Lenient: a final event the server did not terminate with a blank line.
				return strings.Join(lines, "\n"), event, nil
			}
			return "", "", err
		}
		if line == "" {
			if hasData {
				return strings.Join(lines, "\n"), event, nil
			}
			event = ""
			continue
		}
		if line[0] == ':' {
			if s.onComment != nil {
				s.onComment(strings.TrimPrefix(strings.TrimPrefix(line, ":"), " "))
			}
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			lines = append(lines, value)
			hasData = true
		case "event":
			event = value
		default: // id, retry and unknown fields are irrelevant here
		}
	}
}

func wrapReadErr(err error) error {
	switch {
	case errors.Is(err, errStreamTooLarge), errors.Is(err, errLineTooLong):
		return &Error{Kind: KindProtocol, Err: err}
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return &Error{Kind: KindTransient, Message: "stream ended before [DONE]", Err: io.ErrUnexpectedEOF}
	}
	return &Error{Kind: KindTransient, Message: "read stream", Err: err}
}
