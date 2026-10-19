package mail

import (
	"context"
	"errors"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// Body is a message's text, fetched live.
type Body struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

// ReadBody fetches a message's body from the server as plain text without
// marking it read, and returns at most about 8000 characters. The text is never
// stored. ErrGone (404) means the server no longer has it.
func (s *Service) ReadBody(ctx context.Context, id int64) (Message, Body, error) {
	r, err := s.rowByID(ctx, id)
	if err != nil {
		return Message{}, Body{}, err
	}
	if r.Gone || r.UID == 0 || r.Folder == "" {
		return r.message(), Body{}, ErrGone
	}
	a, err := s.account(r.Account)
	if err != nil {
		return Message{}, Body{}, err
	}
	var raw []byte
	err = s.run(ctx, a, func(c *imapclient.Client) error {
		// Reload under the account's lock: an archive or rule may have moved it
		// while this read waited.
		if r, err = s.rowByID(ctx, id); err != nil {
			return err
		}
		if r.Gone || r.UID == 0 || r.Folder == "" {
			return ErrGone
		}
		m, err := fetchRaw(c, r.Folder, r.Validity, []imap.UID{imap.UID(r.UID)}, maxRawBody)
		if err != nil {
			return err
		}
		raw = m[imap.UID(r.UID)]
		return nil
	})
	if errors.Is(err, ErrGone) || (err == nil && len(raw) == 0) {
		if !r.Gone && r.UID != 0 && r.Folder != "" {
			s.markGoneAt(ctx, r)
			s.changed()
		}
		return r.message(), Body{}, ErrGone
	}
	if err != nil {
		return Message{}, Body{}, s.wrapServerErr(err)
	}
	text, err := bodyText(raw)
	if err != nil {
		return r.message(), Body{Text: "(this message has no readable text)"}, nil //nolint:nilerr // an empty message is not a failure
	}
	clipped, truncated := clipRunes(text, maxBodyRunes)
	return r.message(), Body{Text: clipped, Truncated: truncated || int64(len(raw)) >= maxRawBody}, nil
}

// excerpt is the start of a body for a card.
func excerpt(text string) string {
	e, _ := clipRunes(oneLine(text, 4*excerptRunes), excerptRunes)
	return e
}
