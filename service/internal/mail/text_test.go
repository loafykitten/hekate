package mail

import (
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

func TestBodyText(t *testing.T) {
	cases := []struct {
		name, raw, want, not string
	}{
		{"plain", "Subject: x\r\nContent-Type: text/plain\r\n\r\nHello there,\r\n\r\n\r\n\r\nSee you.\r\n", "Hello there,\n\nSee you.", ""},
		{"alternative prefers plain", "Content-Type: multipart/alternative; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nplain version here\r\n--b\r\nContent-Type: text/html\r\n\r\n<p>html version</p>\r\n--b--\r\n", "plain version here", "html version"},
		{"html only", "Content-Type: text/html\r\n\r\n<div>Order <b>shipped</b></div><p>Arrives Friday.</p>", "Order shipped", "<"},
		{"quoted-printable latin1", "Content-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nCaf=E9 au lait is ready now.\r\n", "Café au lait is ready now.", ""},
		{"base64", "Content-Type: text/plain\r\nContent-Transfer-Encoding: base64\r\n\r\nSGVsbG8gZnJvbSBiYXNlNjQgdGV4dA==\r\n", "Hello from base64 text", ""},
		{"attachment skipped", "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nthe message text itself\r\n--b\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=a.txt\r\n\r\nATTACHED SECRET\r\n--b--\r\n", "the message text itself", "ATTACHED"},
		{"truncated multipart", "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nthe first part survives the cut\r\n--b\r\nContent-Type: application/octet-stream\r\n\r\nAAAA", "the first part survives the cut", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := bodyText([]byte(c.raw))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, c.want) || (c.not != "" && strings.Contains(got, c.not)) {
				t.Errorf("got %q, want %q without %q", got, c.want, c.not)
			}
		})
	}
	if _, err := bodyText([]byte("Content-Type: image/png\r\n\r\nPNG")); err == nil {
		t.Error("an image body should report no readable text")
	}
}

func TestPickTrash(t *testing.T) {
	list := func(items ...*imap.ListData) []*imap.ListData { return items }
	box := func(name string, attrs ...imap.MailboxAttr) *imap.ListData {
		return &imap.ListData{Mailbox: name, Attrs: attrs}
	}
	if _, ok := pickTrash(nil); ok {
		t.Error("empty list has no trash")
	}
	// The mail host flags its trash folder \Trash: the flag wins over names.
	got, ok := pickTrash(list(box("Trash"), box("Bin of old stuff", imap.MailboxAttrTrash)))
	if !ok || got != "Bin of old stuff" {
		t.Errorf("flagged = %q %v", got, ok)
	}
	// A server that doesn't flag it is found by a well-known name, ignoring case.
	if got, ok := pickTrash(list(box("INBOX"), box("Deleted Items"))); !ok || got != "Deleted Items" {
		t.Errorf("by name = %q %v", got, ok)
	}
	if _, ok := pickTrash(list(box("INBOX"), box("Archive"))); ok {
		t.Error("no trash folder to find")
	}
	if _, ok := pickTrash(list(box("Trash", imap.MailboxAttrNoSelect))); ok {
		t.Error("an unselectable folder can't be the trash")
	}
	if got, ok := findFolder(list(box("INBOX"), box("Projects")), "projects"); !ok || got != "Projects" {
		t.Errorf("findFolder = %q %v", got, ok)
	}
}
