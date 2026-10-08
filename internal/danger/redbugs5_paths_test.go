package danger

import (
	"testing"
)

// rbPathsAtLeast fails when Classify(cmd) ranks below min.
func rbPathsAtLeast(t *testing.T, cmd string, min RiskClass) {
	t.Helper()
	if got := Classify(cmd); Rank(got) < Rank(min) {
		t.Errorf("Classify(%q) = %s; want at least %s", cmd, got, min)
	}
}

// rbPathsWriteAtLeast fails when ClassifyPathWrite(path) ranks below min.
func rbPathsWriteAtLeast(t *testing.T, path string, min RiskClass) {
	t.Helper()
	if got := ClassifyPathWrite(path); Rank(got) < Rank(min) {
		t.Errorf("ClassifyPathWrite(%q) = %s; want at least %s", path, got, min)
	}
}

// dd to a raw device whose name is not in blockDevicePrefixes, or whose path
// is spelled with `.`, `//` or `..` components, is only system_write (prompt):
//
//	dd if=x of=/dev/md0 bs=1M count=1       -> system_write
//	dd if=x of=/dev/./sda bs=1M count=1     -> system_write
//	dd if=x of=/dev//sda bs=1M count=1      -> system_write
//	dd if=x of=/dev/../dev/sda ...          -> system_write
//	dd if=x of=/dev/mapper/vg-root ...      -> system_write
//	dd if=x of=/dev/mem ...                 -> system_write
//	dd if=x of=/dev/rdisk0 ...              -> system_write   (macOS raw disk)
//
// The shell-redirect spelling `echo x > /dev/md0` (and `> /dev/mem`,
// `> /dev/rdisk0`) is Destructive, and `dd of=/dev/sda` is Blocked/Destructive.
// These must be at least Destructive (deny). A real dd opens the
// kernel-resolved path, so /dev/./sda IS /dev/sda; isBlockDevice /
// containsBlockDevice use raw string prefixes (no path cleaning, short prefix
// list) instead of ClassifyPath, so the deny-class gate is bypassed with a
// trivial path spelling and a single prompt wipes the disk.
func TestRED_Paths_DdToRawDeviceSpellingsNotDestructive(t *testing.T) {
	for _, c := range []string{
		"dd if=x of=/dev/./sda bs=1M count=1",
		"dd if=x of=/dev//sda bs=1M count=1",
		"dd if=x of=/dev/../dev/sda bs=1M count=1",
		"dd if=x of=/dev/md0 bs=1M count=1",
		"dd if=x of=/dev/mapper/vg-root bs=1M count=1",
		"dd if=x of=/dev/mem bs=1M count=1",
		"dd if=x of=/dev/rdisk0 bs=1M count=1",
		"dd if=x of=/dev/s?a bs=1M count=1",
	} {
		rbPathsAtLeast(t, c, Destructive)
	}
}

// dd with a plain output file, or an output path held in a variable, is
// classified Safe (auto-allowed, and proceeds even under non_interactive
// read_only):
//
//	dd if=/dev/zero of=main.go count=1 -> safe   (should be local_write)
//	dd if=x of=$DEV                    -> safe   (should not be safe)
//	dd if=x of="$DEV" bs=1M            -> safe
//
// dd is not in writePrefixes and not in semanticWriteTargets, so an of= value
// that is not itself a sensitive path produces no effect at all. `cp x $DEV`
// and `echo x > $DEV` are Unknown (deny) precisely because a dynamic
// destination is not statically known; dd of=$DEV should fail closed the
// same way, and dd of=<file> is a file overwrite just like `cp x file`.
func TestRED_Paths_DdOutputFileOrVariableIsSafe(t *testing.T) {
	for _, c := range []string{
		"dd if=/dev/zero of=main.go count=1",
		"dd if=x of=./out.bin",
	} {
		rbPathsAtLeast(t, c, LocalWrite)
	}
	for _, c := range []string{
		"dd if=x of=$DEV",
		`dd if=x of="$DEV" bs=1M`,
		"dd if=x of=${DEV} bs=1M",
	} {
		if got := Classify(c); got == Safe || got == LocalWrite {
			t.Errorf("Classify(%q) = %s; dynamic dd destination must fail closed (unknown) like `cp x $DEV`", c, got)
		}
	}
}

// chmodSetsSUIDGID skips every token that starts with "-" as a flag. GNU
// chmod accepts a symbolic mode that begins with '-' (e.g. `-x,u+s`, `-w,g+s`)
// as the mode operand; verified with a real chmod: `chmod -x,u+s f` leaves
// `-rwSr--r--`. Current: local_write (allow). Expected: system_write, the
// same as `chmod u+s f`. Impact: setuid/setgid bit set with no prompt.
func TestRED_Paths_ChmodDashLeadingSymbolicModeSetsSUID(t *testing.T) {
	for _, c := range []string{
		"chmod -x,u+s f",
		"chmod -w,g+s f",
		"chmod -rwx,u+s f",
		"chmod -R -x,u+s dir",
	} {
		rbPathsAtLeast(t, c, SystemWrite)
	}
}

// `install -m 4755 a b`, `install -m u+s a b`, `install -m4755 a b` and
// `mkdir -m 4755 d` all set the setuid bit but classify local_write (allow),
// while the equivalent `chmod 4755 b` is system_write because "a setuid
// binary runs with its owner's privileges ... regardless of the target
// path". Only chmod is passed through chmodSetsSUIDGID. install/mkdir with -m
// (and --mode=) must be treated the same.
func TestRED_Paths_InstallMkdirModeSetuidNotLocalWrite(t *testing.T) {
	for _, c := range []string{
		"install -m 4755 a b",
		"install -m u+s a b",
		"install -m4755 a b",
		"install -Dm4755 a b",
		"install --mode=4755 a b",
		"install --mode=u+s a b",
		"mkdir -m 4755 d",
		"mkdir -m u+s d",
	} {
		rbPathsAtLeast(t, c, SystemWrite)
	}
}

// isWipeTarget recognises `$HOME`, `~/`, `$PWD` (exact) but not other
// spellings of the same catastrophic directories:
//
//	rm -rf "$PWD/"     -> local_write (allow)   but rm -rf "$PWD" is destructive
//	rm -rf ${PWD}/     -> local_write
//	rm -rf ~root       -> local_write           (real shell: /root)
//	rm -rf ~root/      -> local_write
//	rm -rf ~nobody     -> local_write
//
// A real shell expands `~root` to /root and `$PWD/` to the current directory
// with a trailing slash (GNU rm happily deletes the cwd tree through the
// slash form). All must be Destructive like `rm -rf "$PWD"` / `rm -rf ~/`.
func TestRED_Paths_RmWipeTargetSpellingsMissed(t *testing.T) {
	for _, c := range []string{
		`rm -rf "$PWD/"`,
		"rm -rf $PWD/",
		"rm -rf ${PWD}/",
		"rm -rf ~root",
		"rm -rf ~root/",
		"rm -rf ~nobody",
	} {
		rbPathsAtLeast(t, c, Destructive)
	}
}

// expandShellTokenPath expands any leading "~" as `home + path[1:]`, so
// `~root/.bashrc` becomes `/home/userroot/.bashrc` (home concatenated with
// "root/.bashrc") instead of /root/.bashrc. A real shell resolves ~name to
// that user's home, so all of these write another account's startup files:
//
//	echo x >> ~root/.bashrc                      -> local_write (allow)
//	cp x ~root/.bashrc                           -> local_write
//	tee ~root/.profile                           -> local_write
//	echo x > ~root/.config/systemd/user/x.service-> local_write
//
// They must be at least system_write (ideally persistence), exactly like
// `echo x >> ~/.bashrc` and `echo x > /root/.bashrc`.
func TestRED_Paths_TildeUserHomeMisexpanded(t *testing.T) {
	for _, c := range []string{
		"echo x >> ~root/.bashrc",
		"cp x ~root/.bashrc",
		"tee ~root/.profile",
		"echo x > ~root/.zshrc",
		"echo x > ~root/.config/systemd/user/x.service",
		"echo x > ~root/.odek/config.json",
	} {
		rbPathsAtLeast(t, c, SystemWrite)
	}
}

// ClassifyPath / isPersistencePathLexical only protect the CURRENT user's
// home (os.UserHomeDir). Another account's shell rc files and user systemd
// units are plain local_write:
//
//	echo x >> /home/otheruser/.bashrc               local_write
//	echo x > /Users/otheruser/.zshrc                local_write
//	echo x > ~/../otheruser/.bashrc                 local_write
//	/home/otheruser/.config/systemd/user/x.service  local_write
//
// Agents commonly run as root (the docker sandbox default), where writing
// /home/<anyone>/.bashrc is a real login-time persistence/lateral-movement
// primitive. These must be Persistence exactly like the current user's own.
func TestRED_Paths_OtherUsersHomeStartupFilesAreLocalWrite(t *testing.T) {
	for _, p := range []string{
		"/home/zz-other-user/.bashrc",
		"/home/zz-other-user/.profile",
		"/Users/zz-other-user/.zshrc",
		"/home/zz-other-user/.config/systemd/user/x.service",
	} {
		rbPathsWriteAtLeast(t, p, Persistence)
	}
	for _, c := range []string{
		"echo x >> /home/zz-other-user/.bashrc",
		"echo x > /Users/zz-other-user/.zshrc",
		"cp x /home/zz-other-user/.profile",
	} {
		rbPathsAtLeast(t, c, Persistence)
	}
}

// persistenceDirMarkers carry a trailing slash ("/.git/hooks/",
// "/.github/workflows/") but filepath.Clean strips the trailing slash from a
// directory destination, so copying INTO the directory is not seen:
//
//	cp pre-commit .git/hooks/             -> local_write (allow)
//	cp -t .git/hooks pre-commit           -> local_write
//	install -t .git/hooks pre-commit      -> local_write
//	install -m 755 pre-commit .git/hooks  -> local_write
//	mv x .github/workflows                -> local_write
//	ln -sfn /tmp/hooks .git/hooks         -> local_write
//
// whereas `cp pre-commit .git/hooks/pre-commit` is Persistence. The file that
// lands in the directory is the same deferred-execution payload.
func TestRED_Paths_CopyIntoPersistenceDirectoryIsLocalWrite(t *testing.T) {
	for _, c := range []string{
		"cp pre-commit .git/hooks/",
		"cp pre-commit .git/hooks",
		"cp -t .git/hooks pre-commit",
		"cp --target-directory=.git/hooks pre-commit",
		"install -t .git/hooks pre-commit",
		"install -m 755 pre-commit .git/hooks/",
		"mv pre-commit .git/hooks",
		"mv -t .git/hooks pre-commit",
		"cp ci.yml .github/workflows/",
		"mv ci.yml .github/workflows",
		"ln -sfn /tmp/hooks .git/hooks",
	} {
		rbPathsAtLeast(t, c, Persistence)
	}
}

// When the destination is a directory, the file written is
// <dir>/<basename(src)>; classification looks only at the literal operands.
// A payload prepared as ./.bashrc (a local_write) can then be moved into
// $HOME with another auto-allowed command:
//
//	mv .bashrc ~/                  -> local_write
//	cp evil/.bashrc ~/             -> local_write
//	cp -t ~ evil/.zshrc            -> local_write
//	install .profile $HOME/        -> local_write
//
// Each overwrites the user's real shell rc file (persistence). Source
// basenames that are rc files must be resolved against a directory
// destination.
func TestRED_Paths_RcFileCopiedIntoHomeDirectory(t *testing.T) {
	for _, c := range []string{
		"mv .bashrc ~/",
		"cp evil/.bashrc ~/",
		"cp evil/.bashrc ~",
		"cp -t ~ evil/.zshrc",
		"install .profile $HOME/",
		"cp .zshenv $HOME/",
	} {
		rbPathsAtLeast(t, c, Persistence)
	}
}

// commandOnlyReads treats tar as list-only whenever ANY short-flag token
// contains the letter 't' -- including attached option values:
//
//	tar -xf x.tar -C/etc            -> safe   (extracts into /etc; "-C/etc" has a 't')
//	tar -xf x.tar -C/home/user/.ssh -> safe
//	tar -cftest.tar src             -> safe   (creates test.tar; verified with GNU tar)
//	tar -czftmp.tgz src             -> safe
//
// while `tar -xf x.tar -C /etc` is system_write and `tar -cf test.tar src` is
// local_write. Safe means auto-allowed (also in read_only mode).
func TestRED_Paths_TarAttachedValueWithLetterTTreatedAsListing(t *testing.T) {
	for _, c := range []string{
		"tar -cftest.tar src",
		"tar -cvftest.tar src",
		"tar -czftmp.tgz src",
	} {
		rbPathsAtLeast(t, c, LocalWrite)
	}
	for _, c := range []string{
		"tar -xf x.tar -C/etc",
		"tar -xf x.tar -C/home/zz/.ssh",
		"tar -xzf x.tgz -C/usr/local/bin",
	} {
		rbPathsAtLeast(t, c, SystemWrite)
	}
}

// Destination given through an option=value / attached-value spelling is
// skipped because operand scans ignore tokens that start with "-":
//
//	tar -xf x.tar --directory=/etc          local_write   (`-C /etc` = system_write)
//	tar -xf x.tar --directory=/etc/cron.d   local_write
//	unzip x.zip -d/etc/cron.d               local_write   (`-d /etc` = system_write)
//	7z x x.7z -o/etc                        local_write   (`-o /etc` = system_write)
//	git archive --output=/etc/cron.d/x HEAD safe          (`git archive -o ~/.bashrc` = system_write)
//	git archive -o .git/hooks/pre-commit HEAD  safe
//	pandoc x.md --output=/home/user/.bashrc  local_write  (`-o ~/.bashrc` = persistence)
//
// All of these are exact sibling spellings of forms the classifier already
// gates; they must reach the same class.
func TestRED_Paths_OptionEqualsDestinationSpellingsSkipped(t *testing.T) {
	for _, c := range []string{
		"tar -xf x.tar --directory=/etc",
		"tar -xf x.tar --directory=/etc/cron.d",
		"tar --directory=/usr/local/bin -xf x.tar",
		"unzip x.zip -d/etc/cron.d",
		"7z x x.7z -o/etc",
		"git archive --output=/etc/cron.d/x HEAD",
		"git archive --output=/home/user/.bashrc HEAD",
		"pandoc x.md --output=/home/user/.bashrc",
	} {
		rbPathsAtLeast(t, c, SystemWrite)
	}
	for _, c := range []string{
		"git archive -o .git/hooks/pre-commit HEAD",
		"git archive --output=.git/hooks/pre-commit HEAD",
		"tar -xf x.tar --directory=.git/hooks",
		"unzip x.zip -d .git/hooks",
	} {
		rbPathsAtLeast(t, c, Persistence)
	}
}

// rsync is not in writePrefixes, so its destination is never treated as a
// write target; only the trailing-slash systemPathPrefixes fallback fires, and
// only for "/etc/", "/usr/" ... (not "/etc", "/root", "/srv", "/run", "/proc",
// "/sys", "/mnt") and /etc/passwd is explicitly exempted. Result: Safe
// (auto-allowed, even in read_only mode) for
//
//	rsync -a a /etc                         safe
//	rsync -a a /etc/passwd                  safe
//	rsync -a a /root/.bashrc                safe
//	rsync -a a /srv/x                       safe
//	rsync -a a .git/hooks/pre-commit        safe
//	rsync -a a .github/workflows/ci.yml     safe
//	rsync -a a .envrc                       safe
//	rsync -a evil/.bashrc ~/                safe
//
// while `rsync -a a ~/.bashrc` is system_write and `cp a .git/hooks/pre-commit`
// is persistence. (Plain local `rsync -av /src/ /dst/` staying Safe is pinned
// and not asserted here.)
func TestRED_Paths_RsyncToSystemOrPersistenceDestinationIsSafe(t *testing.T) {
	for _, c := range []string{
		"rsync -a a /etc",
		"rsync -a a /etc/passwd",
		"rsync -a a /root/.bashrc",
		"rsync -a a /srv/x",
		"rsync -a a /usr",
		"rsync -a a .git/hooks/pre-commit",
		"rsync -a a .github/workflows/ci.yml",
		"rsync -a a .envrc",
		"rsync -a evil/.bashrc ~/",
	} {
		rbPathsAtLeast(t, c, SystemWrite)
	}
}

// killTargetsInitOrBroadcast compares tokens to the literal strings "1" and
// "-1". The shell's kill builtin / kill(1) parse the operand numerically, so
// leading zeros and a plus sign name the same pids (verified with
// `kill -0 01`, `kill -0 +1`, `kill -0 -- -01` in bash):
//
//	kill -9 01 / kill -9 001 / kill -9 +1  -> local_write (allow)
//	kill -9 -- -01                         -> local_write
//
// while `kill -9 1` / `kill -9 -1` are system_write. As root in a container
// `kill -9 01` terminates pid 1.
func TestRED_Paths_KillInitSpelledWithLeadingZerosOrPlus(t *testing.T) {
	for _, c := range []string{
		"kill -9 01",
		"kill -9 001",
		"kill -9 +1",
		"kill -KILL 01",
		"kill -9 -- -01",
		"kill -s KILL -- -001",
	} {
		rbPathsAtLeast(t, c, SystemWrite)
	}
}

// persistenceBaseNames / persistenceDirMarkers list .github/workflows,
// .gitlab-ci.yml, .travis.yml, .drone.yml and Jenkinsfile but omit other
// mainstream CI definitions that execute on the next push with CI
// credentials. `cp x .circleci/config.yml`, `echo x > azure-pipelines.yml`,
// `bitbucket-pipelines.yml`, `.buildkite/pipeline.yml`, `appveyor.yml` are
// plain local_write (allow) while their listed siblings are Persistence.
func TestRED_Paths_OtherCIPipelineFilesNotPersistence(t *testing.T) {
	for _, p := range []string{
		".circleci/config.yml",
		"azure-pipelines.yml",
		"bitbucket-pipelines.yml",
		".buildkite/pipeline.yml",
		"appveyor.yml",
	} {
		rbPathsWriteAtLeast(t, p, Persistence)
		rbPathsAtLeast(t, "echo x > "+p, Persistence)
	}
}

// .git/config is not a persistence target although it executes commands on
// the next git invocation (core.fsmonitor, core.hooksPath, alias.*=!cmd,
// core.sshCommand, credential.helper) -- the same payload class as
// .git/hooks/*, which IS Persistence, and as `git config core.fsmonitor ...`,
// which is gated as code execution. Hook directories of submodules/worktrees
// (.git/modules/<n>/hooks/) are likewise missed because
// the marker is only "/.git/hooks/".
//
//	echo '[core]' >> .git/config                  -> local_write
//	cp x .git/modules/sub/hooks/pre-commit        -> local_write
func TestRED_Paths_GitConfigAndSubmoduleHooksNotPersistence(t *testing.T) {
	for _, p := range []string{
		".git/config",
		".git/modules/sub/hooks/pre-commit",
	} {
		rbPathsWriteAtLeast(t, p, Persistence)
	}
	for _, c := range []string{
		"echo '[core]' >> .git/config",
		"cp x .git/modules/sub/hooks/pre-commit",
	} {
		rbPathsAtLeast(t, c, Persistence)
	}
}

// Login/startup scripts that run automatically but are missing from
// shellRCFiles / the user-unit marker (all currently local_write, allowed
// silently):
//
//	~/.xinitrc  ~/.xprofile  ~/.xsession  ~/.mkshrc   (X login / mksh start)
//	~/.local/share/systemd/user/x.service
//
// ~/.config/systemd/user/ is Persistence, but systemd's user unit search
// path also includes ~/.local/share/systemd/user/, and .kshrc/.zlogin etc. are
// already listed, so .mkshrc/.xinitrc/.xprofile/.xsession are plain omissions.
func TestRED_Paths_UserLoginScriptsAndLocalShareUnitsNotPersistence(t *testing.T) {
	for _, p := range []string{
		"~/.xinitrc",
		"~/.xprofile",
		"~/.xsession",
		"~/.mkshrc",
		"~/.local/share/systemd/user/x.service",
	} {
		rbPathsWriteAtLeast(t, p, Persistence)
		rbPathsAtLeast(t, "cp x "+p, Persistence)
	}
}
