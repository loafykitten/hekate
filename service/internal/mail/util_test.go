package mail

import (
	"strconv"
	"strings"

	"github.com/emersion/go-imap/v2"
)

func imapUIDs(u ...imap.UID) imap.UIDSet { return imap.UIDSetNum(u...) }

func addSeen() *imap.StoreFlags {
	return &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func mustID(s string) int64 {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		panic(err)
	}
	return n
}
