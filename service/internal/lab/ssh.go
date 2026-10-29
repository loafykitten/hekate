package lab

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const handshakeTimeout = 20 * time.Second

// shellResult is what a command did.
type shellResult struct {
	Exit     int    // -1: no exit status (the connection dropped, or it was cut off here)
	Output   string // stdout and stderr together, cut in the middle when long
	Cut      int    // bytes left out of Output
	TimedOut bool
}

// account is who a command runs as on the lab, and with which key.
type account struct {
	user string
	key  string // path of the private key
	env  string // variables the command runs with, as VAR=value words
}

func (s *Service) hekate() account {
	return account{user: User, key: s.keyPath(), env: "DEBIAN_FRONTEND=noninteractive"}
}

// shell runs command with bash on the lab as User. stdin may be nil (the command reads end of
// file). The time limit holds on the lab's side (timeout(1) kills the command) and here; at most
// maxOut bytes of output are kept, from its start and its end.
func (s *Service) shell(ctx context.Context, command string, stdin io.Reader, limit time.Duration, maxOut int) (shellResult, error) {
	return s.run(ctx, s.hekate(), command, stdin, limit, maxOut)
}

// run is shell as any account.
func (s *Service) run(ctx context.Context, who account, command string, stdin io.Reader, limit time.Duration, maxOut int) (shellResult, error) {
	out := newCapWriter(maxOut)
	exit, timedOut, err := s.exec(ctx, who, command, stdin, out, out, limit)
	if err != nil {
		return shellResult{}, err
	}
	res := shellResult{Exit: exit, TimedOut: timedOut}
	res.Output, res.Cut = out.result()
	return res, nil
}

// exec runs command with bash on the lab as who, its output to stdout and stderr, and returns
// its exit status (-1: none, the connection dropped or it was cut off here) and whether its time
// limit stopped it.
func (s *Service) exec(ctx context.Context, who account, command string, stdin io.Reader, stdout, stderr io.Writer,
	limit time.Duration,
) (exit int, timedOut bool, err error) {
	if !s.Configured() {
		return 0, false, errNotSetUp
	}
	cfg, err := s.clientConfig(who)
	if err != nil {
		return 0, false, err
	}
	secs := int(limit.Round(time.Second) / time.Second)
	remote := who.env + " timeout --kill-after=5 " + strconv.Itoa(secs) + " bash -c " + quote(command)
	ctx, cancel := context.WithTimeout(ctx, limit+handshakeTimeout+10*time.Second)
	defer cancel()

	conn, err := s.dial(ctx, s.addr)
	if err != nil {
		return 0, false, fmt.Errorf("can't reach the lab: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(s.now().Add(handshakeTimeout))
	c, chans, reqs, err := ssh.NewClientConn(conn, s.addr, cfg)
	if err != nil {
		_ = conn.Close()
		return 0, false, fmt.Errorf("can't log in to the lab: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(c, chans, reqs)
	defer func() { _ = client.Close() }()
	sess, err := client.NewSession()
	if err != nil {
		return 0, false, fmt.Errorf("open a session on the lab: %w", err)
	}
	defer func() { _ = sess.Close() }()

	sess.Stdin = stdin
	sess.Stdout = stdout
	sess.Stderr = stderr
	runErr := sess.Run(remote)
	var exitErr *ssh.ExitError
	var missing *ssh.ExitMissingError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		exit = exitErr.ExitStatus()
	case errors.As(runErr, &missing), ctx.Err() != nil:
		exit = -1
	default:
		return 0, false, fmt.Errorf("run on the lab: %w", runErr)
	}
	// timeout(1) exits 124 when it stopped the command, 137 when it had to kill it.
	return exit, ctx.Err() != nil || exit == 124 || exit == 137, nil
}

// clientConfig logs in with the account's key and accepts only the lab's own host key, written
// when the lab was built: nothing is trusted on first use.
func (s *Service) clientConfig(who account) (*ssh.ClientConfig, error) {
	keyPEM, err := readFile(who.key)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.ParsePrivateKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("the lab key: %w", err)
	}
	hostPub, err := readFile(s.hostKeyPath())
	if err != nil {
		return nil, err
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey(hostPub)
	if err != nil {
		return nil, fmt.Errorf("the lab's host key: %w", err)
	}
	return &ssh.ClientConfig{
		User:              who.user,
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   ssh.FixedHostKey(hostKey),
		HostKeyAlgorithms: []string{hostKey.Type()},
		Timeout:           handshakeTimeout,
	}, nil
}

// quote makes s one single-quoted shell word.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// capWriter keeps the first and last bytes written to it, for output that may run long. Stdout
// and stderr write from their own goroutines.
type capWriter struct {
	mu      sync.Mutex
	headMax int
	tailMax int
	head    []byte
	tail    []byte
	total   int
}

func newCapWriter(maxOut int) *capWriter {
	return &capWriter{headMax: maxOut / 3, tailMax: maxOut - maxOut/3}
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += len(p)
	rest := p
	if room := w.headMax - len(w.head); room > 0 {
		n := min(room, len(rest))
		w.head = append(w.head, rest[:n]...)
		rest = rest[n:]
	}
	if len(rest) > 0 {
		w.tail = append(w.tail, rest...)
		if len(w.tail) > 2*w.tailMax {
			w.tail = append([]byte(nil), w.tail[len(w.tail)-w.tailMax:]...)
		}
	}
	return len(p), nil
}

// result is the kept output as text, and how many bytes were left out of its middle.
func (w *capWriter) result() (string, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	tail := w.tail
	if len(tail) > w.tailMax {
		tail = tail[len(tail)-w.tailMax:]
	}
	cut := w.total - len(w.head) - len(tail)
	text := clean(string(w.head))
	if cut > 0 {
		text += fmt.Sprintf("\n[... %d bytes left out ...]\n", cut)
	}
	return text + clean(string(tail)), cut
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// clean makes terminal output plain text: valid UTF-8, no color codes, carriage returns as
// line breaks.
func clean(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = ansi.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}
