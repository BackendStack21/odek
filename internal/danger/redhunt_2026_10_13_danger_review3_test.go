package danger

import (
	"strings"
	"testing"
	"time"
)

// Repeated unterminated escape strings must not make the scan quadratic.
func TestRED_Review3EscapeStringScanScalesLinearly(t *testing.T) {
	timeScan := func(unit string, size int) time.Duration {
		text := strings.Repeat(unit, size/len(unit))
		best := time.Duration(1 << 62)
		for i := 0; i < 3; i++ {
			start := time.Now()
			ScanInjection(text)
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}
	for _, unit := range []string{"\x1bP", "\x1b]", "\x1b_", "\x1b^", "\x1bX", "\x1b[1"} {
		small := timeScan(unit, 32<<10)
		large := timeScan(unit, 512<<10) // 16x the input
		if small < time.Millisecond {
			small = time.Millisecond
		}
		if ratio := float64(large) / float64(small); ratio > 40 {
			t.Errorf("ScanInjection(%q repeated) scaled %.0fx for 16x input (%s -> %s)", unit, ratio, small, large)
		}
	}
}

func TestRED_Review3ParallelBooleanOptionsDoNotSwallowCommand(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, c := range []string{
		`parallel --link make test ::: x`,
		`parallel --lb make test ::: x`,
		`parallel --tmux make test ::: x`,
		`parallel --pipepart --block 1M make test :::: f`,
		`parallel --filter-hosts make test ::: x`,
		`parallel --shebang-wrap make test ::: x`,
		`parallel --number-of-sockets make test ::: x`,
		`parallel --max-procs 4 make test ::: x`,
		`parallel -P 4 make test ::: x`,
		`parallel --jobs 4 make test ::: x`,
	} {
		if got := Classify(c); Rank(got) < Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want at least code_execution", c, got)
		}
	}
	for _, c := range []string{`parallel --link gzip ::: a ::: b`, `parallel --max-procs 4 gzip ::: a.log`, `parallel --lb gzip ::: a`} {
		if got := Classify(c); got != LocalWrite {
			t.Errorf("Classify(%q) = %s, want local_write (as main)", c, got)
		}
	}
	// An option the table does not know leaves the next word as the command,
	// which is classified rather than swallowed.
	if got := Classify(`parallel --not-an-option rm -rf ~ ::: a`); got != Destructive {
		t.Errorf("unknown option hid the command: %s", got)
	}
}

func TestRED_Review3DynamicArgvOptionsAreNotFixedArguments(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	dangerHunt13NotAllowed(t, CodeExecution,
		`cat list | xargs -0 sed -e`,
		`cat list | xargs -0 find .`,
		`cat list | xargs -0 git -C .`,
		`cat list | xargs -I{} git {} x`,
		`cat list | xargs -I{} sed {} README.md`,
		`xargs -a list -0 find`,
	)
}

func TestRED_Review3FindExecPlusOnlyEndsAfterPlaceholder(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	if got := Classify(`find . -exec rm + -rf ~ \;`); got != Destructive {
		t.Errorf("find -exec rm + -rf ~ = %s, want destructive", got)
	}
}

func TestRED_Review3RunnerOptionsAfterVerb(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, c := range []string{`nvm exec --silent 18 rm -rf ~`, `bun x --bun rm -rf ~`, `nvm exec 18 rm -rf ~`} {
		if got := Classify(c); got != Destructive {
			t.Errorf("Classify(%q) = %s, want destructive", c, got)
		}
	}
}

func TestRED_Review3DynamicArgvWorstCase(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	cfg := &DangerousConfig{}
	for _, c := range []string{`nl`, `tac`, `jq`, `wc`, `cat`, `head`, `tail`, `grep`, `rg`, `ls`, `stat`, `basename`, `dirname`, `sha256sum`, `md5sum`, `echo`, `printf`} {
		cmd := `xargs -a list ` + c
		if Classify(cmd) != Safe || cfg.ActionForCommand(cmd) != Allow {
			t.Errorf("%q = %s, want safe", cmd, Classify(cmd))
		}
	}
	for _, c := range []string{`touch`, `gzip`, `gunzip`, `mkdir`, `gofmt`, `black`} {
		cmd := `xargs -a list ` + c
		if got := Classify(cmd); got != LocalWrite {
			t.Errorf("%q = %s, want local_write", cmd, got)
		}
	}
	for _, c := range []string{`curl`, `wget`} {
		cmd := `xargs -a list ` + c
		if got := Classify(cmd); Rank(got) < Rank(NetworkUpload) {
			t.Errorf("%q = %s, want network_upload", cmd, got)
		}
	}
	dangerHunt13NotAllowed(t, CodeExecution, `xargs -a list sort`, `xargs -a list uniq`, `xargs -a list file`,
		`xargs -a list xxd`, `xargs -a list less`, `xargs -a list date`, `xargs -a list docker`)
}

func TestRED_Review3PublishDryRunIsNotUpload(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, c := range []string{`npm publish --dry-run`, `cargo publish --dry-run`, `poetry publish --dry-run`, `npm publish -n`} {
		if got := Classify(c); got == NetworkUpload {
			t.Errorf("Classify(%q) = %s, want no upload", c, got)
		}
	}
	dangerHunt13NotAllowed(t, NetworkUpload, `npm publish`, `cargo publish`)
}

func TestRED_Review3StartupFilesOnlyWritesAreSensitive(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	for _, c := range []string{`cp ~/.emacs ~/emacs.bak`, `rm ~/.vimrc`, `cat ~/.vimrc`, `cp ~/.tmux.conf ./tmux.bak`} {
		if got := Classify(c); Rank(got) > Rank(LocalWrite) {
			t.Errorf("Classify(%q) = %s, want at most local_write", c, got)
		}
	}
	dangerHunt13NotAllowed(t, SystemWrite, `echo x > ~/.vimrc`, `cp x ~/.tmux.conf`, `cat x >> ~/.gdbinit`)
}

func TestRED_Review3AuthSockIsNotASecret(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	t.Chdir(t.TempDir())
	if got := Classify(`echo $SSH_AUTH_SOCK`); got != Safe {
		t.Errorf("echo $SSH_AUTH_SOCK = %s, want safe", got)
	}
	dangerHunt13NotAllowed(t, SystemWrite, `echo $SESSION_COOKIE`, `echo $SLACK_WEBHOOK`)
}
