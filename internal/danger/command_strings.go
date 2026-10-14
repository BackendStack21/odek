package danger

import "strings"

// commandStringPayloads returns the shell command strings that options of a
// command hand to /bin/sh (or run as a program): git -c core.pager=CMD,
// core.sshCommand, alias.X=!CMD and the other program-valued config keys,
// ssh -o ProxyCommand=CMD (and LocalCommand, KnownHostsCommand), tar
// --to-command / -I / --checkpoint-action=exec=CMD, rsync -e CMD, and su/sg
// -c CMD. Each string is a command line in its own right, so the classifier
// judges it and the denylist scans it like a -c payload.
func commandStringPayloads(name string, tokens []string) []string {
	if len(tokens) == 0 {
		return nil
	}
	var out []string
	add := func(s string) {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	switch name {
	case "git":
		for _, o := range gitGlobalOptions.parse(tokens[1:]).opts {
			if !o.has || !o.is("-c") {
				continue
			}
			key, value, ok := strings.Cut(o.value, "=")
			if !ok {
				continue
			}
			key = strings.ToLower(key)
			switch {
			case strings.HasPrefix(key, "alias."):
				if shellCmd, isShell := strings.CutPrefix(value, "!"); isShell {
					add(shellCmd)
				}
			case gitConfigKeyRunsProgram(key), strings.HasPrefix(key, "filter."),
				strings.HasPrefix(key, "diff."), strings.HasPrefix(key, "merge."):
				add(strings.TrimPrefix(value, "!"))
			}
		}
	case "ssh", "scp", "sftp":
		spec := map[string]optSpec{"ssh": sshSyntax, "scp": scpSyntax, "sftp": sftpSyntax}[name]
		for _, o := range spec.parse(tokens[1:]).opts {
			if o.has && o.is("-o") && sshConfigOptionRunsProgram(o.value) {
				key, value, _ := strings.Cut(strings.TrimLeft(o.value, " \t"), "=")
				if !strings.Contains(o.value, "=") {
					key, value, _ = strings.Cut(strings.TrimLeft(o.value, " \t"), " ")
				}
				if k := strings.ToLower(strings.TrimSpace(key)); k == "proxycommand" || k == "localcommand" || k == "knownhostscommand" {
					add(strings.TrimSpace(value))
				}
			}
		}
	case "tar", "bsdtar", "gtar":
		for _, o := range tarOptions.parse(tokens[1:]).opts {
			if !o.has {
				continue
			}
			switch {
			case o.is("-I", "--use-compress-program", "--to-command", "-F", "--info-script",
				"--new-volume-script", "--rsh-command", "--rmt-command"):
				add(o.value)
			case o.is("--checkpoint-action"):
				add(strings.TrimPrefix(o.value, "exec="))
			}
		}
	case "rsync":
		for _, o := range rsyncSyntax.parse(tokens[1:]).opts {
			if o.has && o.unique() && o.is("--rsh") {
				add(o.value)
			}
		}
	case "su", "sg":
		for i := 1; i < len(tokens); i++ {
			t := tokens[i]
			switch {
			case (t == "-c" || t == "--command") && i+1 < len(tokens):
				add(tokens[i+1])
			case strings.HasPrefix(t, "--command="):
				add(strings.TrimPrefix(t, "--command="))
			case strings.HasPrefix(t, "-c") && !strings.HasPrefix(t, "--") && len(t) > 2:
				add(t[2:])
			}
		}
	}
	return out
}
