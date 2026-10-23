package lab

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// ReadOnly says whether a lab_shell command only looks: every command in it (split on |, ;, &&,
// ||) is on a list of reading commands without their writing options, and nothing is redirected
// except to /dev/null. It decides whether a thread turn changed the lab, and it turns away the
// reviewer's commands early with a reason (the reviewer's VM account is what actually stops it).
// It's conservative: shell it doesn't follow (loops, subshells, $(...)) counts as a change, so a
// doubtful command gets its work reviewed. A nil error means read-only.
func ReadOnly(command string) error {
	segs, err := splitShell(command)
	if err != nil {
		return err
	}
	for _, words := range segs {
		for len(words) > 0 && assignment.MatchString(words[0]) {
			words = words[1:]
		}
		if len(words) == 0 {
			continue
		}
		if err := readOnlyWords(words); err != nil {
			return err
		}
	}
	return nil
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// splitShell splits a command line into simple commands, each a list of words with quotes
// removed. It refuses what it can't follow safely.
func splitShell(s string) ([][]string, error) {
	var (
		segs  [][]string
		words []string
		cur   strings.Builder
		in    bool // inside a word
	)
	word := func() {
		if in {
			words = append(words, cur.String())
			cur.Reset()
			in = false
		}
	}
	seg := func() {
		word()
		segs = append(segs, words)
		words = nil
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		next := byte(0)
		if i+1 < len(s) {
			next = s[i+1]
		}
		switch {
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, errors.New("a quote isn't closed")
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
			in = true
		case c == '"':
			i++
			for ; i < len(s) && s[i] != '"'; i++ {
				switch {
				case s[i] == '`' || (s[i] == '$' && i+1 < len(s) && s[i+1] == '('):
					return nil, errors.New("command substitution isn't allowed")
				case s[i] == '\\' && i+1 < len(s):
					i++
				}
				cur.WriteByte(s[i])
			}
			if i >= len(s) {
				return nil, errors.New("a quote isn't closed")
			}
			in = true
		case c == '\\':
			if next != 0 && next != '\n' {
				cur.WriteByte(next)
				in = true
			}
			i++
		case c == '`' || (c == '$' && next == '('):
			return nil, errors.New("command substitution isn't allowed")
		case c == '#' && !in:
			for i < len(s) && s[i] != '\n' {
				i++
			}
			i-- // the newline ends the command
		case c == ' ' || c == '\t':
			word()
		case c == '\n' || c == ';':
			seg()
		case c == '|':
			if next == '|' {
				i++
			}
			seg()
		case c == '&' && next == '&':
			i++
			seg()
		case c == '>' || c == '<' || (c == '&' && next == '>'):
			n, err := redirect(s[i:], &cur, &in)
			if err != nil {
				return nil, err
			}
			i += n - 1
		case c == '&':
			return nil, errors.New("running in the background isn't allowed")
		case c == '(' || c == ')':
			return nil, errors.New("subshells aren't allowed")
		default:
			cur.WriteByte(c)
			in = true
		}
	}
	seg()
	return segs, nil
}

// redirect reads one redirection starting at s (the word before it may hold its descriptor) and
// returns how many bytes it took. Only discarding output is allowed: >/dev/null, 2>&1 and the like.
func redirect(s string, cur *strings.Builder, in *bool) (int, error) {
	if *in {
		if fd := cur.String(); strings.Trim(fd, "0123456789") == "" {
			cur.Reset() // a descriptor, not a word
			*in = false
		} else {
			return 0, errors.New("a redirection must be separated from the word before it")
		}
	}
	if s[0] == '<' {
		return 0, errors.New("input redirection isn't allowed")
	}
	i := 1
	if s[0] == '&' {
		i = 2 // &>
	}
	if i < len(s) && s[i] == '>' {
		i++ // >>
	}
	if i < len(s) && s[i] == '&' {
		j := i + 1
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j == i+1 {
			return 0, errors.New("redirect to a descriptor, like 2>&1")
		}
		return j, nil
	}
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	j := i
	for j < len(s) && !strings.ContainsRune(" \t\n;|&<>", rune(s[j])) {
		j++
	}
	if s[i:j] != "/dev/null" {
		return 0, fmt.Errorf("output may only be redirected to /dev/null, not %q", s[i:j])
	}
	return j, nil
}

// readers only read whatever their arguments.
var readers = setOf("cat", "ls", "head", "tail", "grep", "egrep", "fgrep", "zgrep", "zcat", "wc", "cut",
	"tr", "column", "nl", "fold", "rev", "tac", "comm", "diff", "cmp", "md5sum", "sha1sum", "sha256sum",
	"sha512sum", "base64", "hexdump", "od", "strings", "jq", "readlink", "realpath", "basename",
	"dirname", "which", "whereis", "type", "id", "whoami", "groups", "uname", "nproc", "lscpu", "getent", "dig",
	"nslookup", "host", "sleep", "stat", "du", "df", "free", "uptime", "vmstat", "iostat", "lsof",
	"ss", "netstat", "lsblk", "findmnt", "ps", "pgrep", "lsmod", "modinfo", "dpkg-query", "printenv",
	"locale", "last", "w", "who", "getfacl", "lsattr", "echo", "printf", "true", "false", ":", "test", "[", "cd",
	"export", "set", "debsecan", "pwd")

// readOnlyWords checks one simple command.
func readOnlyWords(w []string) error {
	name := path.Base(w[0])
	args := w[1:]
	refuse := func() error {
		return fmt.Errorf("%q changes things or isn't on the read-only list", strings.Join(w, " "))
	}
	// short reports whether a short option among letters is given, alone, bundled (-qo) or with
	// its value attached (-ofile). A letter inside an attached value counts too: wrongly refused
	// means reviewed, never missed.
	short := func(letters string) bool {
		return slices.ContainsFunc(args, func(a string) bool { return shortBundle(a) && strings.ContainsAny(a[1:], letters) })
	}
	has := func(bad ...string) bool {
		for _, a := range args {
			for _, b := range bad {
				if a == b || (strings.HasPrefix(b, "--") && strings.HasPrefix(a, b+"=")) {
					return true
				}
			}
		}
		return false
	}
	switch {
	case readers[name]:
		return nil
	case name == "sudo":
		for len(args) > 0 && args[0] == "-n" {
			args = args[1:]
		}
		if len(args) == 0 || strings.HasPrefix(args[0], "-") {
			return refuse()
		}
		return readOnlyWords(args)
	case name == "timeout":
		args, ok := skipOptions(args, "-s", "-k")
		if !ok || len(args) < 2 {
			return refuse()
		}
		return readOnlyWords(args[1:])
	case name == "xargs":
		args, ok := skipOptions(args, "-n", "-L", "-P", "-d", "-I", "-a", "-E", "-s")
		if !ok {
			return refuse()
		}
		if len(args) == 0 {
			return nil // echo
		}
		return readOnlyWords(args)
	case name == "uniq":
		// uniq [input [output]]: a second file is written.
		if positional(args, "-f", "-s", "-w", "--skip-fields", "--skip-chars", "--check-chars") > 1 {
			return refuse()
		}
		return nil
	case name == "xxd":
		// xxd [input [output]], and -r turns a dump back into a file.
		if slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, "-r") }) || positional(args, "-c", "-g", "-l", "-o", "-s", "-n", "-cols", "-groupsize", "-len", "-seek", "-name") > 1 {
			return refuse()
		}
		return nil
	case name == "yq":
		// -i edits in place; -s (--split-exp) writes a file per document.
		if short("is") || has("--inplace", "--split-exp") {
			return refuse()
		}
		return nil
	case name == "tree":
		if short("oR") || has("--output") {
			return refuse()
		}
		return nil
	case name == "rg":
		if has("--pre", "--search-zip", "-z") {
			return refuse() // --pre runs a program on each file
		}
		return nil
	case name == "file":
		if short("C") || has("--compile") {
			return refuse()
		}
		return nil
	case name == "apt-cache":
		return subcommand(args, refuse, "show", "showpkg", "showsrc", "policy", "search", "depends", "rdepends",
			"madison", "pkgnames", "stats", "dump")
	case name == "command":
		if len(args) > 0 && (args[0] == "-v" || args[0] == "-V") {
			return nil
		}
		return refuse()
	case name == "env":
		if len(args) == 0 {
			return nil
		}
		return refuse()
	case name == "hostname":
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				return refuse() // sets it
			}
		}
		return nil
	case name == "date":
		if short("s") || has("--set") {
			return refuse()
		}
		return nil
	case name == "dmesg":
		if short("cCnDE") || has("--clear", "--read-clear", "--console-level", "--console-off", "--console-on") {
			return refuse()
		}
		return nil
	case name == "top":
		if has("-b") {
			return nil
		}
		return refuse()
	case name == "ping":
		if has("-c") {
			return nil
		}
		return refuse()
	case name == "mount":
		if len(args) == 0 {
			return nil
		}
		return refuse()
	case name == "sort":
		if short("o") || has("--output") {
			return refuse()
		}
		return nil
	case name == "find":
		if has("-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprint0", "-fprintf", "-fls") {
			return refuse()
		}
		return nil
	case name == "sed":
		for _, a := range args {
			if strings.HasPrefix(a, "-i") || strings.HasPrefix(a, "--in-place") || sedWrites.MatchString(a) {
				return refuse()
			}
		}
		return nil
	case name == "awk" || name == "gawk" || name == "mawk":
		for _, a := range args {
			if a == "-i" || strings.Contains(a, "system") || strings.Contains(a, "getline") ||
				strings.ContainsAny(a, "|>") {
				return refuse()
			}
		}
		return nil
	case name == "curl":
		return curlReadOnly(args, refuse)
	case name == "wget":
		for i, a := range args {
			if a == "--spider" || a == "-O-" || a == "-qO-" || a == "--output-document=-" ||
				(a == "-O" && i+1 < len(args) && args[i+1] == "-") {
				return nil
			}
		}
		return refuse()
	case name == "systemctl":
		return subcommand(args, refuse, "status", "is-active", "is-enabled", "is-failed", "list-units",
			"list-unit-files", "list-timers", "list-sockets", "show", "cat", "get-default")
	case name == "journalctl":
		if has("--vacuum-size", "--vacuum-time", "--vacuum-files", "--rotate", "--flush", "--sync",
			"--relinquish-var", "--smart-relinquish-var", "--setup-keys", "--update-catalog") {
			return refuse()
		}
		return nil
	case name == "ip":
		if has("add", "del", "delete", "set", "change", "replace", "flush", "exec", "append", "prepend",
			"save", "restore", "attach", "detach", "up", "down") {
			return refuse()
		}
		return nil
	case name == "nft":
		return subcommand(args, refuse, "list")
	case name == "iptables" || name == "ip6tables":
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "-S", "-L", "-n", "-v", "-x", "--line-numbers", "--list", "--list-rules", "--numeric", "--verbose":
			case "-t", "--table":
				i++
			default:
				if strings.HasPrefix(args[i], "-") {
					return refuse()
				}
			}
		}
		return nil
	case name == "iptables-save" || name == "ip6tables-save":
		return nil
	case name == "tailscale":
		return subcommand(args, refuse, "status", "ip", "netcheck", "version", "whois")
	case name == "sysctl":
		for _, a := range args {
			if a == "-w" || a == "--write" || a == "-p" || a == "--load" || a == "--system" || strings.Contains(a, "=") {
				return refuse()
			}
		}
		return nil
	case name == "apt" || name == "apt-get":
		return subcommand(args, refuse, "list", "show", "policy", "search", "depends", "rdepends", "changelog")
	case name == "dpkg":
		for _, a := range args {
			if strings.HasPrefix(a, "-") && !slices.Contains([]string{"-l", "-L", "-s", "-S", "-V", "-p",
				"--list", "--listfiles", "--status", "--search", "--verify", "--print-avail", "--get-selections"}, a) {
				return refuse()
			}
		}
		return nil
	case name == "timedatectl" || name == "hostnamectl" || name == "networkctl" || name == "resolvectl" || name == "loginctl":
		if len(args) == 0 {
			return nil
		}
		return subcommand(args, refuse, "status", "show", "list", "query", "statistics", "timesync-status",
			"list-sessions", "list-users", "show-session", "show-user")
	case name == "crontab":
		if len(args) == 1 && args[0] == "-l" {
			return nil
		}
		return refuse()
	case name == "docker":
		return dockerReadOnly(args, refuse)
	case name == "docker-compose":
		return composeReadOnly(args, refuse)
	}
	return refuse()
}

// sedWrites matches a sed script that writes a file or runs a command (w, W, e).
var sedWrites = regexp.MustCompile(`(^|[;{}/0-9$\s])[wWe](\s|$)`)

// subcommand allows the command when its first word that isn't an option is one of ok.
func subcommand(args []string, refuse func() error, ok ...string) error {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if slices.Contains(ok, a) {
			return nil
		}
		return refuse()
	}
	return refuse()
}

// curlReadOnly allows requests that send nothing and save nothing.
func curlReadOnly(args []string, refuse func() error) error {
	long := []string{"--request", "--data", "--data-raw", "--data-binary", "--data-urlencode", "--data-ascii",
		"--form", "--form-string", "--upload-file", "--output", "--remote-name", "--remote-name-all", "--config",
		"--json", "--cookie-jar", "--dump-header", "--output-dir", "--create-dirs", "--trace", "--trace-ascii",
		"--stderr", "--etag-save", "--hsts", "--alt-svc"}
	for _, a := range args {
		switch {
		case strings.HasPrefix(a, "--"):
			for _, l := range long {
				if a == l || strings.HasPrefix(a, l+"=") {
					return refuse()
				}
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			// Short options may be bundled (-sSLo); the ones taking a value end the bundle.
			for _, r := range a[1:] {
				if strings.ContainsRune("XdFToOKcDb", r) {
					return refuse()
				}
				if strings.ContainsRune("HuAemwrxUEy", r) {
					break // takes a value: the rest of the bundle is it
				}
			}
		}
	}
	return nil
}

// dockerReadOnly allows docker commands that only look.
func dockerReadOnly(args []string, refuse func() error) error {
	if len(args) == 0 {
		return nil
	}
	sub, rest := args[0], args[1:]
	follows := func() bool {
		return slices.ContainsFunc(rest, func(a string) bool { return a == "-f" || a == "--follow" })
	}
	switch sub {
	case "-v", "--version", "version", "info", "ps", "images", "inspect", "top", "port", "diff":
		return nil
	case "logs":
		if follows() {
			return refuse()
		}
		return nil
	case "stats":
		if slices.Contains(rest, "--no-stream") {
			return nil
		}
		return refuse()
	case "manifest":
		if len(rest) > 0 && rest[0] == "inspect" {
			return nil
		}
	case "buildx":
		if len(rest) > 1 && rest[0] == "imagetools" && rest[1] == "inspect" {
			return nil
		}
		if len(rest) > 0 && (rest[0] == "ls" || rest[0] == "version") {
			return nil
		}
	case "network", "volume", "image", "container", "system", "context":
		if len(rest) == 0 {
			return refuse()
		}
		switch rest[0] {
		case "ls", "list", "inspect", "history", "df", "top", "port", "diff", "info", "show":
			return nil
		case "logs":
			if follows() {
				return refuse()
			}
			return nil
		}
	case "compose":
		return composeReadOnly(rest, refuse)
	}
	return refuse()
}

// composeReadOnly allows docker compose commands that only look.
func composeReadOnly(args []string, refuse func() error) error {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		switch args[i] {
		case "-f", "--file", "-p", "--project-name", "--project-directory", "--env-file", "--profile", "--ansi", "--progress":
			i += 2
		default:
			i++
		}
	}
	if i >= len(args) {
		return refuse()
	}
	rest := args[i+1:]
	switch args[i] {
	case "config":
		// -o and --output write a file, in any spelling: -o x, -ox, -qo x, --output=x.
		if slices.ContainsFunc(rest, func(a string) bool {
			return strings.HasPrefix(a, "--output") || (shortBundle(a) && strings.ContainsRune(a, 'o'))
		}) {
			return refuse()
		}
		return nil
	case "ps", "ls", "images", "top", "port", "version":
		return nil
	case "logs":
		if slices.ContainsFunc(rest, func(a string) bool { return a == "-f" || a == "--follow" }) {
			return refuse()
		}
		return nil
	}
	return refuse()
}

// skipOptions drops the options before a command's operands; those named take a value, which
// must be there.
func skipOptions(args []string, valued ...string) ([]string, bool) {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") {
		if slices.Contains(valued, args[0]) {
			if len(args) < 2 {
				return nil, false
			}
			args = args[1:]
		}
		args = args[1:]
	}
	return args, true
}

// positional counts the operands among args; the options named take a value.
func positional(args []string, valued ...string) int {
	n := 0
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case slices.Contains(valued, a):
			i++
		case a == "-" || !strings.HasPrefix(a, "-"):
			n++
		}
	}
	return n
}

// shortBundle reports whether a is a cluster of short options, like -qo.
func shortBundle(a string) bool { return len(a) > 1 && a[0] == '-' && a[1] != '-' }

func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}
