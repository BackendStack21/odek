package danger

import (
	"net/url"
	"strings"
)

// Network uploads.
//
// NetworkEgress covers every command that touches a socket and is allowed by
// default, which is right for fetching and cloning but wrong for the commands
// that ship local content out or let a remote party in. NetworkUpload is the
// prompting half. A command is an upload when one of these holds:
//
//   - its request body comes from a file, from stdin (a redirect, a here-string,
//     or a pipe from a producer that is not a literal echo/printf), or from a
//     runtime substitution or variable — an inline literal body such as
//     `curl -d '{"a":1}' URL` is plain egress, because the approver already
//     sees every byte of it on the command line;
//   - it presents credentials or a client certificate (curl -u/-n/--cert,
//     wget --http-password/--load-cookies, ...);
//   - it uses a mutating method (curl -X POST, wget --method=PUT, ...);
//   - it speaks a protocol that sends or mutates by nature (smtp, telnet,
//     dict, gopher, ldap);
//   - it is a transfer whose source is local and whose destination is remote
//     (scp, rsync, rclone, aws s3 cp, gsutil cp, ...); a remote source with a
//     local destination is a download and stays egress;
//   - it opens a listener or a tunnel (nc -l, socat *-LISTEN, ssh -L/-R/-D/-w,
//     rsync --daemon), or forwards the agent or X11 to the remote side.
//
// Running a command on the remote host (`ssh host ls`) is deliberately left as
// egress: the command text is on the command line and nothing local is sent.
// Every upload also carries NetworkEgress so a policy that denies egress still
// denies the upload; the two effects are evaluated independently.

// splitStdinRedirect removes redirections from args and reports whether the
// command's stdin is fed from a file, a here-document/string or another file
// descriptor. `< /dev/null` feeds nothing and does not count.
func splitStdinRedirect(args []string) (rest []string, stdinData bool) {
	isInput := func(tok string) bool {
		switch tok {
		case "<", "<<", "<<-", "<<<", "<>", "<&":
			return true
		}
		return false
	}
	isDigit := func(tok string) bool { return len(tok) == 1 && tok[0] >= '0' && tok[0] <= '9' }
	for i := 0; i < len(args); i++ {
		tok := args[i]
		// The tokenizer drops the adjacency of `2>file`, so a single digit
		// before a redirection is read as its file descriptor and removed;
		// longer numbers (`nc host 80 < f`) stay operands.
		if isDigit(tok) && i+1 < len(args) && (isInput(args[i+1]) || isRedirectToken(args[i+1])) {
			if isInput(args[i+1]) && tok != "0" {
				// Not stdin, but fail closed on any input redirect below.
				tok = args[i+1]
				i++
			} else {
				i++
				tok = args[i]
			}
		}
		switch {
		case isInput(tok):
			target := ""
			if i+1 < len(args) {
				i++
				target = args[i]
			}
			switch {
			case tok == "<&":
				stdinData = stdinData || (target != "-" && target != "0")
			case target == "/dev/null":
			default:
				stdinData = true
			}
		case isRedirectToken(tok):
			if i+1 < len(args) {
				i++
			}
		default:
			rest = append(rest, args[i])
		}
	}
	return
}

// stdinFeed describes where a pipeline stage's stdin comes from.
type stdinFeed struct {
	// piped reports that an upstream pipe stage feeds stdin.
	piped bool
	// static reports that the upstream producer is a literal echo/printf.
	static bool
}

// carriesData reports whether stdin feeds the command local content: a
// redirect from a file or here-string, or a pipe from anything but a literal
// echo/printf.
func (f stdinFeed) carriesData(redirected bool) bool {
	return redirected || (f.piped && !f.static)
}

func hasDynamicSubstitution(v string) bool { return strings.Contains(v, dynamicSubstToken) }

// hasVariableReference reports whether v still holds a shell variable
// reference after static expansion, so its value is only known at run time.
func hasVariableReference(v string) bool {
	for i := 0; i < len(v); i++ {
		if v[i] == '$' {
			if name, _ := variableReference(v, i); name != "" {
				return true
			}
		}
	}
	return false
}

// transferVerdict is the set of extra effects a network client invocation
// carries beyond plain egress.
type transferVerdict struct {
	upload  bool
	code    bool
	system  bool
	unknown bool
	// read is the class of a local file read through a file:// URL.
	read RiskClass
}

func (v transferVerdict) effects() []RiskClass {
	var out []RiskClass
	if v.upload {
		out = append(out, NetworkUpload)
	}
	if v.code {
		out = append(out, CodeExecution)
	}
	if v.system {
		out = append(out, SystemWrite)
	}
	if v.unknown {
		out = append(out, Unknown)
	}
	if v.read != "" && v.read != Safe {
		out = append(out, v.read)
	}
	return out
}

// networkTransferEffects returns the effects a network client invocation
// carries on top of the plain NetworkEgress isNetworkEgress reports. inner is
// the command with execution wrappers removed; feed describes its stdin.
func networkTransferEffects(name string, inner []string, feed stdinFeed) []RiskClass {
	if len(inner) == 0 {
		return nil
	}
	args, redirected := splitStdinRedirect(inner[1:])
	stdin := feed.carriesData(redirected)
	var v transferVerdict
	switch name {
	case "curl":
		v = curlTransfer(args)
	case "wget":
		v = wgetTransfer(args)
	case "scp":
		v = scpTransfer(args)
	case "rsync":
		v = rsyncTransfer(args)
	case "sftp":
		v = sftpTransfer(args, stdin)
	case "ssh":
		v = sshTransfer(args, stdin)
	case "nc", "ncat":
		v = netcatTransfer(args, stdin)
	case "socat":
		v = socatTransfer(args, stdin)
	case "telnet":
		v.upload = stdin
	case "ftp", "tftp":
		v = ftpTransfer(args, stdin)
	case "openssl":
		v = opensslTransfer(args, stdin)
	case "rclone":
		v = rcloneTransfer(args)
	case "gh":
		v = ghTransfer(args, stdin)
	case "aws", "gsutil", "gcloud", "az":
		v.upload = cloudUploadForm(name, args)
	case "dig", "nslookup", "host", "drill", "ping", "ping6", "traceroute", "traceroute6":
		v.unknown = dnsQueryCarriesRuntimeData(name, args)
	}
	return v.effects()
}

// ── curl ────────────────────────────────────────────────────────────

var curlSyntax = optSpec{
	abbrev: true,
	short:  "AbcCdDeEFHKmoPQrtTuUwxXyYz",
	alias: map[byte]string{
		'A': "--user-agent", 'b': "--cookie", 'd': "--data", 'e': "--referer",
		'E': "--cert", 'F': "--form", 'H': "--header", 'n': "--netrc",
		'Q': "--quote", 'T': "--upload-file", 'u': "--user", 'U': "--proxy-user",
		'X': "--request",
	},
	long: longTable(
		"abstract-unix-socket alt-svc aws-sigv4 cacert capath cert cert-type ciphers config connect-timeout "+
			"connect-to continue-at cookie cookie-jar create-file-mode crlfile curves data data-ascii data-binary "+
			"data-raw data-urlencode delegation dns-interface dns-ipv4-addr dns-ipv6-addr dns-servers doh-url "+
			"dump-header ech egd-file engine expect100-timeout form form-string ftp-account "+
			"ftp-alternative-to-user ftp-method ftp-port ftp-ssl-ccc-mode happy-eyeballs-timeout-ms "+
			"haproxy-clientip header hostpubmd5 hostpubsha256 hsts interface ip-tos ipfs-gateway json "+
			"keepalive-time key key-type krb libcurl limit-rate local-port login-options mail-auth mail-from "+
			"mail-rcpt max-filesize max-redirs max-time netrc-file noproxy oauth2-bearer output output-dir "+
			"parallel-max pass pinnedpubkey preproxy proto proto-default proto-redir proxy proxy-cacert "+
			"proxy-capath proxy-cert proxy-cert-type proxy-ciphers proxy-crlfile proxy-header proxy-key "+
			"proxy-key-type proxy-pass proxy-pinnedpubkey proxy-service-name proxy-tls13-ciphers "+
			"proxy-tlsauthtype proxy-tlspassword proxy-tlsuser proxy-user pubkey quote random-file range rate "+
			"referer request request-target resolve retry retry-delay retry-max-time sasl-authzid service-name "+
			"socks4 socks4a socks5 socks5-gssapi-service socks5-hostname speed-limit speed-time stderr "+
			"tftp-blksize time-cond tls-max tls13-ciphers tlsauthtype tlspassword tlsuser trace trace-ascii "+
			"unix-socket upload-file url url-query user user-agent variable vlan-priority write-out "+
			"expand-data expand-form expand-header expand-json expand-url expand-output expand-request "+
			"expand-user expand-cookie expand-referer expand-upload-file expand-write-out expand-url-query",
		"netrc netrc-optional get head include insecure location silent show-error fail remote-name "+
			"remote-name-all remote-header-name compressed verbose cert-status help version",
	),
}

// curlRuntimeDataOptions are the options whose value is sent to the server: a
// command substitution in one of them carries local data out. Output paths
// (-o, -D, -c, ...) are deliberately absent.
var curlRuntimeDataOptions = []string{
	"--header", "--user-agent", "--referer", "--cookie", "--data", "--data-ascii", "--data-binary",
	"--data-raw", "--data-urlencode", "--form", "--form-string", "--json", "--url", "--url-query",
	"--proxy-header", "--request", "--user", "--proxy-user", "--oauth2-bearer", "--upload-file",
	"--quote", "--variable",
}

// curlSchemeUploads are URL schemes whose use sends or mutates by nature.
var curlSchemeUploads = fieldSet("smtp smtps telnet dict gopher gophers ldap ldaps")

// curlHostGuess are the host-name prefixes from which curl guesses a protocol
// when the URL has no scheme.
var curlHostGuess = []string{"smtp.", "dict.", "ldap."}

func curlTransfer(args []string) transferVerdict {
	var v transferVerdict
	r := curlSyntax.parse(args)
	operands := r.args()
	urls := append([]string(nil), operands...)
	for _, o := range r.opts {
		val := o.value
		for _, n := range o.names {
			base := n
			if strings.HasPrefix(n, "--expand-") {
				base = "--" + strings.TrimPrefix(n, "--expand-")
			}
			switch base {
			case "--upload-file", "--netrc", "--netrc-file", "--netrc-optional",
				"--user", "--proxy-user", "--oauth2-bearer",
				"--cert", "--key", "--proxy-cert", "--proxy-key",
				"--quote", "--mail-from", "--mail-rcpt", "--mail-auth":
				v.upload = true
			case "--data", "--data-ascii", "--data-binary":
				if strings.HasPrefix(val, "@") || hasVariableReference(val) {
					v.upload = true
				}
			case "--data-raw", "--form-string":
				if hasVariableReference(val) {
					v.upload = true
				}
			case "--data-urlencode":
				if curlURLEncodeReadsFile(val) || hasVariableReference(val) {
					v.upload = true
				}
			case "--json":
				if strings.HasPrefix(val, "@") || hasVariableReference(val) {
					v.upload = true
				}
			case "--form":
				if curlFormReadsFile(val) || hasVariableReference(val) {
					v.upload = true
				}
			case "--request":
				if mutatingMethod(val) {
					v.upload = true
				}
			case "--url":
				urls = append(urls, val)
			}
		}
		if hasDynamicSubstitution(val) && o.is(curlRuntimeDataOptions...) {
			v.upload = true
		}
	}
	for _, u := range urls {
		if hasDynamicSubstitution(u) {
			v.upload = true
		}
		curlClassifyURL(u, &v)
	}
	return v
}

func curlURLEncodeReadsFile(val string) bool {
	eq, at := strings.IndexByte(val, '='), strings.IndexByte(val, '@')
	return at >= 0 && (eq < 0 || at < eq)
}

func curlFormReadsFile(val string) bool {
	_, rest, ok := strings.Cut(val, "=")
	return ok && (strings.HasPrefix(rest, "@") || strings.HasPrefix(rest, "<"))
}

// mutatingMethod reports whether an HTTP method (or an unknown spelling,
// which fails closed) changes remote state.
func mutatingMethod(m string) bool {
	switch strings.ToUpper(strings.TrimSpace(m)) {
	case "GET", "HEAD", "OPTIONS", "TRACE":
		return false
	}
	return true
}

func curlClassifyURL(u string, v *transferVerdict) {
	lower := strings.ToLower(u)
	if scheme, _, ok := strings.Cut(lower, "://"); ok {
		if curlSchemeUploads[scheme] {
			v.upload = true
		}
	} else {
		for _, prefix := range curlHostGuess {
			if strings.HasPrefix(lower, prefix) {
				v.upload = true
			}
		}
	}
	if strings.HasPrefix(lower, "file:") {
		v.read = worstOf(v.read, fileURLReadClass(u[len("file:"):]))
	}
}

// fileURLReadClass classifies the local read a file: URL performs the way a
// cat of the same path would be classified.
func fileURLReadClass(rest string) RiskClass {
	rest = strings.TrimLeft(rest, "/")
	rest = strings.TrimPrefix(rest, "localhost/")
	path := "/" + strings.TrimLeft(rest, "/")
	if decoded, err := url.PathUnescape(path); err == nil {
		path = decoded
	}
	cls := classifyResourceToken(path)
	if touchesSystemPath([]string{path}) {
		cls = worstOf(cls, SystemWrite)
	}
	return cls
}

// ── wget ────────────────────────────────────────────────────────────

var wgetSyntax = optSpec{
	abbrev: true,
	short:  "aoeOiBtTwQPlARDIXUn",
	alias:  map[byte]string{'e': "--execute", 'O': "--output-document"},
	long: longTable(
		"execute post-data post-file body-data body-file method header user password http-user http-password "+
			"proxy-user proxy-password ftp-user ftp-password load-cookies save-cookies certificate private-key "+
			"ca-certificate output-document output-file append-output input-file base tries timeout wait "+
			"waitretry directory-prefix user-agent referer accept reject domains exclude-domains "+
			"include-directories exclude-directories level quota limit-rate bind-address certificate-type "+
			"private-key-type ca-directory crl-file secure-protocol config default-page use-askpass "+
			"dns-timeout connect-timeout read-timeout cut-dirs restrict-file-names backups",
		"continue no-clobber recursive no-parent quiet verbose spider mirror",
	),
}

func wgetTransfer(args []string) transferVerdict {
	var v transferVerdict
	r := wgetSyntax.parse(args)
	operands := r.args()
	for _, o := range r.opts {
		val := o.value
		for _, n := range o.names {
			switch n {
			case "--execute":
				// A wgetrc command can redirect output, add proxies, headers
				// or credentials, or name a post file. Only the everyday
				// robots toggle is harmless.
				if k := strings.ToLower(strings.ReplaceAll(val, " ", "")); k != "robots=off" && k != "robots=on" {
					v.system = true
				}
			case "--post-file", "--body-file", "--load-cookies",
				"--http-user", "--http-password", "--user", "--password",
				"--proxy-user", "--proxy-password", "--ftp-user", "--ftp-password",
				"--certificate", "--private-key":
				v.upload = true
			case "--post-data", "--body-data":
				if hasVariableReference(val) {
					v.upload = true
				}
			case "--method":
				if mutatingMethod(val) {
					v.upload = true
				}
			}
		}
		if hasDynamicSubstitution(val) && o.is("--header", "--post-data", "--body-data", "--user-agent", "--referer") {
			v.upload = true
		}
	}
	for _, u := range operands {
		if hasDynamicSubstitution(u) {
			v.upload = true
		}
	}
	return v
}

// ── scp / rsync / sftp / rclone ─────────────────────────────────────

// uploadByDirection reports whether the operands carry a local source before
// a remote destination: some local operand precedes a remote one. A remote
// source with a local destination (a download) and remote-to-remote copies
// are not uploads.
func uploadByDirection(operands []string, remote func(string) bool) bool {
	seenLocal := false
	for _, op := range operands {
		if remote(op) {
			if seenLocal {
				return true
			}
			continue
		}
		seenLocal = true
	}
	return false
}

// colonBeforeSlash reports whether op has a colon that is not preceded by a
// slash: the `host:path` form scp and rsync read as remote, while `./a:b`
// names a local file.
func colonBeforeSlash(op string) bool {
	colon := strings.IndexByte(op, ':')
	if colon < 0 {
		return false
	}
	slash := strings.IndexByte(op, '/')
	return slash < 0 || colon < slash
}

func scpRemote(op string) bool {
	return strings.HasPrefix(op, "scp://") || strings.HasPrefix(op, "[") || colonBeforeSlash(op)
}

func rsyncRemote(op string) bool {
	return strings.HasPrefix(op, "rsync://") || strings.HasPrefix(op, "[") || colonBeforeSlash(op)
}

var scpSyntax = optSpec{abbrev: true, short: "cDFiJloPSX"}

func scpTransfer(args []string) transferVerdict {
	operands := scpSyntax.parse(args).args()
	return transferVerdict{upload: uploadByDirection(operands, scpRemote)}
}

var rsyncSyntax = optSpec{
	abbrev: true,
	short:  "efBTM@",
	alias:  map[byte]string{'e': "--rsh"},
	long: longTable(
		"rsh rsync-path exclude include exclude-from include-from filter files-from port bwlimit timeout "+
			"contimeout log-file log-file-format password-file temp-dir compare-dest copy-dest link-dest "+
			"backup-dir suffix max-size min-size block-size chmod chown usermap groupmap partial-dir out-format "+
			"info debug iconv address sockopts protocol checksum-choice checksum-seed compress-level "+
			"compress-choice max-delete remote-option read-batch write-batch only-write-batch stop-after "+
			"stop-at modify-window bwlimit early-input fuzzy-basis",
		"daemon archive recursive verbose compress delete dry-run progress partial",
	),
}

func rsyncTransfer(args []string) transferVerdict {
	r := rsyncSyntax.parse(args)
	operands := r.args()
	v := transferVerdict{upload: uploadByDirection(operands, rsyncRemote)}
	for _, o := range r.opts {
		if o.is("--daemon") {
			v.upload = true
		}
	}
	return v
}

var sftpSyntax = optSpec{abbrev: true, short: "BbcDFiJlOoPRsSX"}

func sftpTransfer(args []string, stdin bool) transferVerdict {
	v := transferVerdict{upload: stdin}
	r := sftpSyntax.parse(args)
	for _, o := range r.opts {
		// A batch file is a command script that may put files.
		if o.is("-b") {
			v.upload = true
		}
	}
	return v
}

var rcloneSyntax = optSpec{
	abbrev: true,
	long: longTable(
		"config transfers checkers bwlimit exclude include filter exclude-from include-from filter-from "+
			"files-from log-file log-level min-age max-age min-size max-size retries low-level-retries timeout "+
			"contimeout backup-dir suffix tpslimit user-agent drive-impersonate buffer-size order-by",
		"progress verbose quiet dry-run recursive",
	),
}

func rcloneRemote(op string) bool { return colonBeforeSlash(op) }

func rcloneTransfer(args []string) transferVerdict {
	operands := rcloneSyntax.parse(args).args()
	if len(operands) == 0 {
		return transferVerdict{}
	}
	switch operands[0] {
	case "copy", "copyto", "sync", "move", "moveto", "bisync":
		return transferVerdict{upload: uploadByDirection(operands[1:], rcloneRemote)}
	case "rcat", "serve":
		// rcat streams stdin to the remote; serve opens a listener.
		return transferVerdict{upload: true}
	}
	return transferVerdict{}
}

// ── ssh and socket tools ────────────────────────────────────────────

var sshSyntax = optSpec{abbrev: true, short: "BbcDEeFIiJLlmOoPpQRSWw", operandLimit: 1}

// sshChannelConfigKeys are ssh_config keywords that open a forward or tunnel
// or hand the remote side the local agent or display.
var sshChannelConfigKeys = fieldSet("remoteforward localforward dynamicforward tunnel forwardagent forwardx11 forwardx11trusted")

func sshTransfer(args []string, stdin bool) transferVerdict {
	v := transferVerdict{upload: stdin}
	r := sshSyntax.parse(args)
	for _, o := range r.opts {
		switch {
		case o.is("-L", "-R", "-D", "-w", "-W", "-N", "-A", "-X", "-Y"):
			v.upload = true
		case o.is("-o"):
			key := strings.ToLower(strings.TrimLeft(o.value, " \t"))
			val := ""
			if end := strings.IndexAny(key, "= \t"); end >= 0 {
				val = strings.ToLower(strings.Trim(key[end:], "= \t\""))
				key = key[:end]
			}
			if sshChannelConfigKeys[key] && val != "no" {
				v.upload = true
			}
		}
	}
	return v
}

var netcatSyntax = optSpec{
	abbrev: true,
	short:  "pswiIOPqTXxecmV",
	long: longTable(
		"exec sh-exec lua-exec source-port source wait delay proxy proxy-type proxy-auth ssl-cert ssl-key "+
			"ssl-trustfile ssl-ciphers ssl-servername ssl-alpn allow allowfile deny denyfile max-conns "+
			"idle-timeout output hex-dump append-output connect-timeout",
		"listen keep-open broker chat ssl udp sctp send-only recv-only nodns verbose telnet crlf",
	),
}

func netcatTransfer(args []string, stdin bool) transferVerdict {
	v := transferVerdict{upload: stdin}
	r := netcatSyntax.parse(args)
	for _, o := range r.opts {
		switch {
		case o.is("-l", "--listen", "--broker", "--chat"):
			v.upload = true
		case o.is("-e", "-c", "--exec", "--sh-exec", "--lua-exec"):
			v.code = true
		}
	}
	return v
}

func socatTransfer(args []string, stdin bool) transferVerdict {
	v := transferVerdict{upload: stdin}
	for _, tok := range args {
		if strings.HasPrefix(tok, "-") && tok != "-" {
			continue
		}
		kind := strings.ToUpper(tok)
		if end := strings.IndexAny(kind, ":,"); end >= 0 {
			kind = kind[:end]
		}
		switch {
		case kind == "EXEC" || kind == "SYSTEM":
			v.code = true
		case strings.Contains(kind, "LISTEN") || strings.Contains(kind, "RECV") || kind == "TUN":
			v.upload = true
		case kind == "FILE" || kind == "OPEN" || kind == "GOPEN" || kind == "CREATE" || kind == "PIPE":
			v.upload = true
		}
	}
	return v
}

func ftpTransfer(args []string, stdin bool) transferVerdict {
	v := transferVerdict{upload: stdin}
	for _, tok := range args {
		lower := strings.ToLower(tok)
		if lower == "put" || lower == "mput" || strings.HasPrefix(lower, "put ") || strings.HasPrefix(lower, "mput ") {
			v.upload = true
		}
	}
	return v
}

func opensslTransfer(args []string, stdin bool) transferVerdict {
	for _, tok := range args {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		switch tok {
		case "s_client":
			return transferVerdict{upload: stdin}
		case "s_server":
			return transferVerdict{upload: true}
		}
		break
	}
	return transferVerdict{}
}

// ── gh and cloud CLIs ───────────────────────────────────────────────

var ghSyntax = optSpec{
	abbrev: true,
	short:  "RFfXHt",
	long: longTable(
		"repo field raw-field method header input jq template hostname preview cache paginate-limit",
		"paginate silent include slurp",
	),
	alias: map[byte]string{'R': "--repo", 'F': "--field", 'f': "--raw-field", 'X': "--method", 'H': "--header"},
}

func ghTransfer(args []string, stdin bool) transferVerdict {
	r := ghSyntax.parse(args)
	operands := r.args()
	if len(operands) == 0 {
		return transferVerdict{}
	}
	sub := operands[0]
	second := ""
	if len(operands) > 1 {
		second = operands[1]
	}
	switch {
	case sub == "gist" && second == "create", sub == "release" && second == "upload":
		return transferVerdict{upload: true}
	case sub == "api":
		for _, o := range r.opts {
			switch {
			case o.is("--input"):
				return transferVerdict{upload: true}
			case o.is("--field") && curlFormReadsFile(o.value):
				return transferVerdict{upload: true}
			case o.is("--method") && mutatingMethod(o.value):
				return transferVerdict{upload: true}
			}
		}
	}
	return transferVerdict{}
}

// cloudStorageScheme reports whether op names an object-store location.
func cloudStorageScheme(op string) bool {
	for _, p := range []string{"s3://", "gs://", "gcs://", "az://", "abfs://", "abfss://", "wasbs://"} {
		if strings.HasPrefix(strings.ToLower(op), p) {
			return true
		}
	}
	return false
}

func cloudCopyOperands(tokens []string) []string {
	var out []string
	for _, t := range tokens {
		if t == "-" || !strings.HasPrefix(t, "-") {
			out = append(out, t)
		}
	}
	return out
}

// cloudUploadForm recognises only the narrow upload forms of the cloud CLIs:
// a local source copied to an object-store destination (aws s3 cp/mv/sync,
// gsutil cp/mv/rsync, gcloud storage cp/mv/rsync), aws s3api put-object with
// a --body, and az storage blob/file upload commands. Everything else on
// these CLIs keeps its existing classification.
func cloudUploadForm(name string, args []string) bool {
	switch name {
	case "aws":
		var words []string
		for i := 0; i < len(args); i++ {
			t := args[i]
			if strings.HasPrefix(t, "-") && t != "-" {
				switch strings.TrimPrefix(strings.TrimPrefix(t, "-"), "-") {
				case "profile", "region", "endpoint-url", "output", "query", "ca-bundle", "color",
					"cli-read-timeout", "cli-connect-timeout", "cli-binary-format":
					if !strings.Contains(t, "=") {
						i++
					}
				}
				continue
			}
			words = append(words, t)
		}
		if len(words) >= 2 && words[0] == "s3" {
			switch words[1] {
			case "cp", "mv", "sync":
				return uploadByDirection(cloudCopyOperands(words[2:]), cloudStorageScheme)
			}
		}
		if len(words) >= 2 && words[0] == "s3api" && (words[1] == "put-object" || words[1] == "upload-part") {
			for _, t := range args {
				if t == "--body" || strings.HasPrefix(t, "--body=") {
					return true
				}
			}
		}
	case "gsutil":
		for i, t := range args {
			if strings.HasPrefix(t, "-") {
				continue
			}
			switch t {
			case "cp", "mv", "rsync":
				return uploadByDirection(cloudCopyOperands(args[i+1:]), cloudStorageScheme)
			}
			return false
		}
	case "gcloud":
		for i, t := range args {
			if t == "storage" && i+1 < len(args) {
				switch args[i+1] {
				case "cp", "mv", "rsync":
					return uploadByDirection(cloudCopyOperands(args[i+2:]), cloudStorageScheme)
				}
			}
		}
	case "az":
		if len(args) > 0 && args[0] == "storage" {
			for _, t := range args[1:] {
				switch t {
				case "upload", "upload-batch", "append", "sync":
					return true
				}
			}
		}
	}
	return false
}

// ── DNS tools ───────────────────────────────────────────────────────

// dnsQueryCarriesRuntimeData reports whether a lookup's name (or server)
// holds a command substitution or an unresolved variable: the queried name
// then carries data chosen at run time, which is a DNS exfiltration channel.
// dig -f takes its queries from a file, which cannot be inspected. The same
// holds for the reachability probes (ping, traceroute), whose destination name
// is a DNS query too. Literal names stay plain egress.
func dnsQueryCarriesRuntimeData(name string, args []string) bool {
	numeric := probeNumericOptions[name]
	for i, a := range args {
		if name == "dig" && digReadsQueryFile(a) {
			return true
		}
		if !hasDynamicSubstitution(a) && !hasVariableReference(a) {
			continue
		}
		// A count, timeout or hop limit is not a destination or payload.
		if i > 0 && numeric != "" && isShortFlagToken(args[i-1]) && len(args[i-1]) == 2 && strings.IndexByte(numeric, args[i-1][1]) >= 0 {
			continue
		}
		if len(a) > 2 && isShortFlagToken(a) && numeric != "" && strings.IndexByte(numeric, a[1]) >= 0 && !hasDynamicSubstitution(a) {
			continue
		}
		return true
	}
	return false
}

// probeNumericOptions lists, per tool, the short options whose value is a
// number (count, interval, timeout, size, TTL), so a variable there does not
// make the destination unknown. ping's -p pattern and the address options are
// absent: their values reach the wire.
var probeNumericOptions = map[string]string{
	"ping": "cwWistmQ", "ping6": "cwWistmQ",
	"traceroute": "mqwft", "traceroute6": "mqwft",
}

// digReadsQueryFile reports whether a dig argument is the -f batch option,
// which takes its queries from a file.
func digReadsQueryFile(arg string) bool {
	return strings.HasPrefix(arg, "-f") && !strings.HasPrefix(arg, "--")
}
