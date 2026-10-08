package danger

import "testing"

// rb5execAction returns the default-policy action for cmd.
func rb5execAction(cmd string) Action {
	return (&DangerousConfig{}).ActionForCommand(cmd)
}

// rb5execMustNotAllow fails for every command the default policy silently
// allows. All commands passed here run code, mutate remote/shared state, or
// write outside the working tree in a real shell.
func rb5execMustNotAllow(t *testing.T, why string, cmds []string) {
	t.Helper()
	for _, c := range cmds {
		if got := rb5execAction(c); got == Allow {
			t.Errorf("ActionForCommand(%q) = allow (effects %v); %s", c, Analyze(c).Effects, why)
		}
	}
}

// tar -F / --info-script / --new-volume-script run an arbitrary script every
// time tar switches to the next volume (with -M/--multi-volume). Verified
// with GNU tar 1.35: `tar -cM -L 10 -F ./hook.sh -f v1.tar big` executes
// hook.sh, and so do the fused (-F./hook.sh) and long spellings.
//
// Today: `tar -xf a.tar -M -F/tmp/x.sh` classifies safe and the separated /
// long forms classify local_write (all auto-allowed). Expected: code_execution
// (prompt), like --to-command / --use-compress-program, which tarRunsCommand
// (classifier.go ~4773) already gates but whose list omits the volume-script
// options.
func TestRED_TarVolumeScriptOptionsRunCommands(t *testing.T) {
	rb5execMustNotAllow(t, "tar volume scripts execute an arbitrary program", []string{
		"tar -xf a.tar -M -F/tmp/x.sh",
		"tar -xf a.tar -M -F /tmp/x.sh",
		"tar -xf a.tar --info-script=/tmp/x.sh -M",
		"tar -cf a.tar --new-volume-script=/tmp/x.sh -M x",
		"tar -cf a.tar --new-volume-script /tmp/x.sh -M x",
	})
}

// GNU tar accepts any unambiguous prefix of a long option, and old-style /
// bundled option clusters let -I take its argument from a later word.
// Verified with GNU tar 1.35 (hook script logged an invocation for each):
//
//	tar -xf a.tar --use-compress-prog=./hook.sh
//	tar -xf a.tar --to-com=./hook.sh
//	tar -xf a.tar --checkpoint=1 --checkpoint-act=exec=./hook.sh
//	tar xIf ./hook.sh a.tar        (I consumes ./hook.sh, f consumes a.tar)
//	tar xvfI a.tar ./hook.sh
//
// Today all of these classify local_write (auto-allowed) because
// tarRunsCommand only matches the full option names and a leading "-I".
// Expected: code_execution.
func TestRED_TarAbbreviatedAndBundledCommandOptions(t *testing.T) {
	rb5execMustNotAllow(t, "GNU tar runs the helper program; abbreviation/bundling is not a different command", []string{
		"tar -xf a.tar --use-compress-prog=/tmp/x.sh",
		"tar -xf a.tar --to-com=/tmp/x.sh",
		"tar -xf a.tar --checkpoint=1 --checkpoint-act=exec=/tmp/x.sh",
		"tar xIf /tmp/x.sh a.tar",
		"tar xvfI a.tar /tmp/x.sh",
	})
}

// GNU sed accepts `!` negation, `first~step`, `addr1,~N`, `addr1,addr2` regex
// ranges and the `I` address flag before a command. The `e` command is
// still just `e`. Verified with GNU sed 4.9: every one of these runs
// `echo PWN` through the shell:
//
//	sed '1!e echo PWN' f        sed '$!e echo PWN' f     sed '/x/!e echo PWN' f
//	sed '1~2e echo PWN' f       sed '2,~4e echo PWN' f
//	sed '/a/,/b/e echo PWN' f   sed '/a/I e echo PWN' f  sed '1,/b/e echo PWN' f
//
// Today they classify safe (auto-allowed) because the standalone-`e` regexp
// in sedScriptHasShellExec (classifier.go ~4416) only recognises numeric
// ranges and a single /re/ prefix, while `sed '1,3e id'` is caught.
// Expected: code_execution.
func TestRED_SedExecuteCommandAfterComplexAddress(t *testing.T) {
	rb5execMustNotAllow(t, "sed 'e' command executes shell code regardless of address form", []string{
		"sed '1!e echo PWN' f",
		"sed '$!e echo PWN' f",
		"sed '/x/!e echo PWN' f",
		"sed '1~2e echo PWN' f",
		"sed '2,~4e echo PWN' f",
		"sed '/a/,/b/e echo PWN' f",
		"sed '/a/I e echo PWN' f",
		"sed '1,/b/e echo PWN' f",
	})
}

// `git bisect run <cmd>` executes <cmd> once per bisection step, and
// `git hook run <name>` (git >= 2.36) executes the repository's hook script.
// Verified with git 2.43: `git bisect run sh -c 'echo X >> log; exit 0'`
// appends to log, and `git hook run pre-commit` runs .git/hooks/pre-commit.
//
// Today both classify safe (auto-allowed), while `git commit` / `git merge`
// / `git add` (which only might run a hook) are code_execution via the git
// case in adapterRunsCode (command_effects.go:42), which has no
// `bisect`/`hook` entry. Expected: code_execution.
func TestRED_GitBisectRunAndHookRunAreCodeExecution(t *testing.T) {
	rb5execMustNotAllow(t, "git runs the supplied command / repo hook", []string{
		"git bisect run sh -c 'echo hi'",
		"git bisect run ./test.sh",
		"git -C repo bisect run make test",
		"git hook run pre-commit",
	})
}

// `git clone --config key=value` (and --config=key=value) applies the config
// to the new repository before the fetch/checkout, exactly like
// `git clone -c key=value`. Verified with git 2.43:
// `git clone --config=core.hooksPath=/tmp/hp /tmp/src dst` ran
// /tmp/hp/post-checkout during the clone. Likewise `git clone --template=DIR`
// copies DIR/hooks into the new repo and post-checkout runs at the end of the
// clone (verified).
//
// Today `git clone -c core.sshCommand=...` is code_execution, but the
// `--config` spellings and --template are plain network_egress
// (auto-allowed): isGitCodeExecution (classifier.go ~3700) only matches
// `-c` / `--config-env`. Expected: code_execution for the config-override
// spellings and the template clone.
func TestRED_GitCloneLongConfigAndTemplateRunCode(t *testing.T) {
	rb5execMustNotAllow(t, "clone --config/--template injects hooks/ssh commands just like clone -c", []string{
		"git clone --config=core.hooksPath=/tmp/hp /tmp/src dst",
		"git clone --config core.hooksPath=/tmp/hp /tmp/src dst",
		"git clone --config=core.sshCommand=/tmp/ssh.sh host:repo dst",
		"git clone --config alias.x=!/tmp/x.sh host:repo dst",
		"git clone --template=/tmp/tpl /tmp/src dst",
	})
	// Control: the -c spelling is already gated.
	if got := rb5execAction("git clone -c core.sshCommand=/tmp/ssh.sh host:repo dst"); got == Allow {
		t.Errorf("control: -c spelling unexpectedly allowed")
	}
}

// --upload-pack / --receive-pack / --exec name a program git runs locally to
// talk to the "remote" (any local path or ssh-less URL), and
// remote.<name>.uploadpack does the same from config. Verified with git
// 2.43 using a local repo: each of these invoked the wrapper script:
//
//	git clone --upload-pack=./up.sh SRC dst      git clone -u ./up.sh SRC dst
//	git fetch --upload-pack=./up.sh SRC          git pull --upload-pack=./up.sh SRC master
//	git ls-remote --upload-pack=./up.sh SRC      git -c remote.origin.uploadpack=./up.sh fetch
//	git push --receive-pack=./rp.sh BARE master  git push --exec=./rp.sh BARE master
//
// Today all classify network_egress (auto-allowed). Expected: code_execution.
// gitCodeExecConfigKeys / isGitCodeExecution know core.sshcommand but not
// these program-valued options.
func TestRED_GitUploadPackReceivePackOptionsRunPrograms(t *testing.T) {
	rb5execMustNotAllow(t, "git runs the named program locally", []string{
		"git clone --upload-pack=/tmp/up.sh /tmp/src dst",
		"git clone -u /tmp/up.sh /tmp/src dst",
		"git fetch --upload-pack=/tmp/up.sh /tmp/src",
		"git pull --upload-pack=/tmp/up.sh /tmp/src master",
		"git ls-remote --upload-pack=/tmp/up.sh /tmp/src",
		"git -c remote.origin.uploadpack=/tmp/up.sh fetch",
		"git push --receive-pack=/tmp/rp.sh /tmp/bare.git master",
		"git push --exec=/tmp/rp.sh /tmp/bare.git master",
	})
}

// More git config keys whose value is a program git spawns. gitCodeExecConfigKeys
// (classifier.go:3688) lists core.sshcommand / credential.helper / core.pager
// but not the siblings below, so `git -c <key>=<prog> fetch` is plain
// network_egress (auto-allowed):
//
//	core.askPass       - run to obtain a password on https auth
//	core.gitProxy      - proxy command for git:// connections
//	credential.<url>.helper - URL-scoped credential helper ("!prog" runs a shell)
//
// Expected: code_execution, the same as `-c credential.helper=...`.
func TestRED_GitConfigProgramKeysSiblingsOfSshCommand(t *testing.T) {
	rb5execMustNotAllow(t, "program-valued git config key via -c", []string{
		"git -c core.askPass=/tmp/a.sh fetch",
		"git -c core.gitProxy=/tmp/p.sh fetch",
		"git -c 'credential.https://example.com.helper=!/tmp/h.sh' fetch",
	})
}

// Refspec spellings that force-update or delete remote refs. `git push
// --force` is system_write, but these are equivalent history/ref
// destruction on the remote and stay network_egress (auto-allowed):
//
//	git push origin +main          (leading + = forced update, see git-push(1))
//	git push origin +HEAD:main
//	git push --mirror origin       (force-overwrites and deletes every remote ref)
//	git push --delete origin main  / git push -d origin main
//	git push origin :main          (empty source = delete remote ref)
//	git push --prune origin 'refs/heads/*'
//
// isGitDataLoss's push case (classifier.go ~3955) only checks --force,
// --force-with-lease and -f clusters. Expected: system_write (prompt).
func TestRED_GitPushForceByRefspecMirrorDeleteIsDataLoss(t *testing.T) {
	rb5execMustNotAllow(t, "force/delete push rewrites remote history like --force", []string{
		"git push origin +main",
		"git push origin +HEAD:main",
		"git push --mirror origin",
		"git push --delete origin main",
		"git push -d origin main",
		"git push origin :main",
		"git push --prune origin 'refs/heads/*'",
	})
}

// `git maintenance start` / `register` (git >= 2.30) install a recurring
// background job (crontab entry, launchd plist or systemd user timers) and
// write maintenance.* config. The git binary's own strings show it shells out
// to `crontab`; with a fake crontab on PATH, `git maintenance start
// --scheduler=crontab` invoked `crontab -l` (verified). That is persistence.
//
// Also, `--output=<file>` on log/show/archive writes an arbitrary file:
// `git archive -o /etc/x.tar` is system_write but `git archive --output=/etc/x.tar`
// and `git log --output=/etc/profile.d/x.sh` (content partly attacker
// controlled via commit messages) classify safe.
//
// Today both groups are safe (auto-allowed). Expected: persistence /
// system_write (prompt).
func TestRED_GitMaintenanceStartAndOutputFileWrites(t *testing.T) {
	rb5execMustNotAllow(t, "git maintenance start installs a scheduled job", []string{
		"git maintenance start",
		"git maintenance register",
	})
	rb5execMustNotAllow(t, "--output writes an arbitrary path (same as archive -o)", []string{
		"git archive --output=/etc/x.tar HEAD",
		"git log --output=/etc/profile.d/x.sh",
		"git show --output=/etc/cron.d/x HEAD",
	})
}

// Safe-listed filters that run a caller-chosen program. rg --pre is already
// gated (adapterRunsCode); these are the same shape and classify safe
// (auto-allowed). Verified locally (GNU sort 9.4 / diffutils sdiff 3.10 /
// ripgrep 14.1) with logging wrapper scripts, each ran the program:
//
//	sort -S 1k --compress-program=./c.sh big.txt   (runs c.sh for temp files)
//	sdiff --diff-program=./d.sh a b
//	rg --hostname-bin=./h.sh --hyperlink-format=default foo f
//
// Expected: code_execution.
func TestRED_SafeListedToolsWithProgramOptions(t *testing.T) {
	rb5execMustNotAllow(t, "option names a helper program the tool executes", []string{
		"sort -S 1k --compress-program=/tmp/c.sh big.txt",
		"sort --compress-program /tmp/c.sh big.txt",
		"sdiff --diff-program=/tmp/d.sh a b",
		"sdiff --diff-program /tmp/d.sh a b",
		"rg --hostname-bin=/tmp/h.sh --hyperlink-format=default foo",
		"rg --hostname-bin /tmp/h.sh foo",
	})
}

// ssh/scp/sftp/rsync run a local command via ProxyCommand / LocalCommand /
// -S program / -e remote-shell, per ssh_config(5), scp(1) and rsync(1)
// (`-e, --rsh=COMMAND` is executed locally as the transport). git's
// core.sshCommand is gated as code_execution for the same reason, but these
// direct spellings are plain network_egress (auto-allowed):
//
//	ssh -o ProxyCommand=prog host         ssh -oProxyCommand=prog host
//	ssh -o 'ProxyCommand prog' host       ssh -F ./evil_config host
//	ssh -o LocalCommand=prog -o PermitLocalCommand=yes host
//	scp -S ./prog a h:b                   scp -o ProxyCommand=prog a h:b
//	sftp -S ./prog h
//	rsync -e ./prog h:src dst             rsync --rsh=./prog h:src dst
//
// Expected: code_execution.
func TestRED_SshFamilyLocalCommandOptions(t *testing.T) {
	rb5execMustNotAllow(t, "option makes the client execute a local program", []string{
		"ssh -o ProxyCommand=/tmp/p.sh host",
		"ssh -oProxyCommand=/tmp/p.sh host",
		"ssh -o 'ProxyCommand /tmp/p.sh' host",
		"ssh -F /tmp/evil_config host",
		"ssh -o LocalCommand=/tmp/p.sh -o PermitLocalCommand=yes host",
		"scp -S /tmp/p.sh a h:b",
		"scp -o ProxyCommand=/tmp/p.sh a h:b",
		"sftp -S /tmp/p.sh h",
		"rsync -e /tmp/p.sh h:src dst",
		"rsync --rsh=/tmp/p.sh h:src dst",
	})
}

// classifyInfraCLI takes the first non-flag token as the verb and never
// skips the VALUE of a value-taking global flag, so:
//
//	kubectl -n get exec pod -- sh        verb "get"  -> network_egress (allowed)
//	                                     kubectl really runs `exec` in namespace "get"
//	kubectl -n get delete pods --all     -> network_egress, really `delete`
//	kubectl --context logs apply -f x    -> network_egress, really `apply`
//	helm -n list uninstall rel           -> network_egress, really `uninstall`
//
// and the same mistake makes ordinary commands deny (unknown):
//
//	kubectl -n kube-system get pods      verb "kube-system" -> unknown (deny)
//	kubectl --namespace kube-system get pods
//	kubectl --context prod describe pod x
//	helm -n x list
//
// (the `-n=x` / `--namespace=x` spellings are fine). Expected: the real verb
// decides: exec/delete/apply/uninstall prompt, get/list/describe allow.
func TestRED_InfraCLIGlobalFlagValueIsNotTheVerb(t *testing.T) {
	rb5execMustNotAllow(t, "the real verb is exec/delete/apply/uninstall; the flag value was read as the verb", []string{
		"kubectl -n get exec pod -- sh",
		"kubectl -n get delete pods --all",
		"kubectl --context logs apply -f x.yaml",
		"helm -n list uninstall rel",
	})
	for _, c := range []string{
		"kubectl -n kube-system get pods",
		"kubectl --namespace kube-system get pods",
		"kubectl --context prod describe pod x",
		"helm -n x list",
	} {
		if got := rb5execAction(c); got != Allow {
			t.Errorf("ActionForCommand(%q) = %s (class %s); read-only get/describe/list with a namespace/context flag should match the flagless form (allow)", c, got, Classify(c))
		}
	}
}

// Same root cause in classifyHugo and the docker-compose flag table.
//
//	hugo -s site server / hugo --source site serve / hugo --poll 1s server
//	  `hugo server` is code_execution, but a flag value before the verb is
//	  taken as the verb ("site", "1s") and the default is local_write (allowed).
//	docker compose --progress plain up
//	docker-compose --host tcp://h:2375 up / --context c / --log-level DEBUG
//	  `--progress`, `--host`/-H, `--context`, `--log-level`, `--tls*` are
//	  value-taking compose flags missing from containerComposeFlagsWithArg
//	  (classifier.go:4851); the value becomes the "verb" and `up` (which runs
//	  images, prompt) is unknown (deny) instead.
func TestRED_HugoAndComposeFlagValuesTakenAsVerb(t *testing.T) {
	rb5execMustNotAllow(t, "hugo server was hidden behind a flag value", []string{
		"hugo -s site server",
		"hugo --source site serve",
		"hugo --poll 1s server",
	})
	for _, c := range []string{
		"docker compose --progress plain up",
		"docker-compose --host tcp://h:2375 up",
		"docker-compose --context c up",
		"docker-compose --log-level DEBUG up",
	} {
		if got := rb5execAction(c); got != Prompt {
			t.Errorf("ActionForCommand(%q) = %s (class %s); want prompt, same as `docker compose up`", c, got, Classify(c))
		}
	}
}

// helm accepts --post-renderer (also =spelling) on template/install/upgrade:
// the named executable receives the rendered manifests. `helm install` /
// `upgrade` are system_write (prompt), but `helm template` is listed
// read-only network_egress (auto-allowed) and executes the renderer.
// Expected: code_execution.
func TestRED_HelmPostRendererRunsProgram(t *testing.T) {
	rb5execMustNotAllow(t, "helm --post-renderer executes a local program", []string{
		"helm template x ./chart --post-renderer ./evil.sh",
		"helm template x ./chart --post-renderer=./evil.sh",
	})
}

// classifyInfraCLI buckets whole verbs. Siblings that mutate persistent
// state sit in the auto-allowed network_egress bucket while their analogues
// are system_write:
//
//	terraform state rm|mv|push|replace-provider  -> network_egress, but
//	  terraform import / taint / untaint are system_write (all edit state)
//	kubectl auth reconcile -f rbac.yaml          -> network_egress (the verb is
//	  "auth"), but it creates/updates RBAC roles on the cluster, like `kubectl apply`
//
// Expected: system_write for the mutating sub-verbs; `terraform state
// list|show|pull` and `kubectl auth can-i` stay allowed.
func TestRED_InfraMutatingSubverbsNotEgress(t *testing.T) {
	rb5execMustNotAllow(t, "mutates state/cluster like the verbs classified system_write", []string{
		"terraform state rm aws_instance.x",
		"terraform state mv aws_instance.a aws_instance.b",
		"terraform state push terraform.tfstate",
		"terraform state replace-provider a/b c/d",
		"kubectl auth reconcile -f rbac.yaml",
	})
	for _, c := range []string{"terraform state list", "terraform state show aws_instance.x", "terraform state pull", "kubectl auth can-i get pods"} {
		if got := rb5execAction(c); got != Allow {
			t.Errorf("control: ActionForCommand(%q) = %s, want allow", c, got)
		}
	}
}
