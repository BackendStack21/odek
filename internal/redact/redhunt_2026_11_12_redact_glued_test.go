package redact

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// A command word that follows a JSON \n escape starts a command, and so does
// a later command word that the earlier command's words reach over (an
// unpaired quote glues the two): both passwords are replaced.
func TestRED_CommandPasswordAfterEarlierCommandOnSameLine(t *testing.T) {
	in := `"x\nhttp -a user:Qz7KMafUn4qlp GET x.example "http -a user:Qz7nOgM9tDi GET x.example"`
	want := `"x\nhttp -a user:[REDACTED] GET x.example "http -a user:[REDACTED] GET x.example"`
	if out := RedactSecrets(in); out != want {
		t.Errorf("in  =%s\n out =%s\n want=%s", in, out, want)
	}
}

// gluedCommands returns lines of n commands glued by each combination of
// glues, inside every frame, and the passwords planted in each. Command k
// uses the tool step places after its predecessor's, so step 0 repeats one
// tool.
func gluedCommands(n, step int) (lines []string, passwords [][]string) {
	tools := []string{
		"curl -u user:%s https://x.example",
		"curl -su user:%s https://x.example",
		"curl --user user:%s https://x.example",
		"wget --password=%s https://x.example",
		"http -a user:%s GET x.example",
		"https -a user:%s GET x.example",
		"xh -a user:%s GET x.example",
	}
	glues := []string{" && ", "; ", " | ", `\n`, `\n `, ` "`, `" `, `" "`, ` '`, `' `, `\" `, ` \"`, ` "x\n`}
	frames := []string{"%s", `"%s`, `%s"`, `"%s"`, `"x\n%s`, `"x\n%s"`, `{"command":"%s"}`, `'%s`, `sh -c '%s'`, `sh -c "%s"`}
	combos := 1
	for k := 1; k < n; k++ {
		combos *= len(glues)
	}
	for first := range tools {
		for c := 0; c < combos; c++ {
			var b strings.Builder
			var pws []string
			g := c
			for k := 0; k < n; k++ {
				if k > 0 {
					b.WriteString(glues[g%len(glues)])
					g /= len(glues)
				}
				pw := fmt.Sprintf("Qz7K%dMafUn4q%dlp", k, first)
				pws = append(pws, pw)
				b.WriteString(fmt.Sprintf(tools[(first+k*step)%len(tools)], pw))
			}
			for _, frame := range frames {
				lines = append(lines, strings.Replace(frame, "%s", b.String(), 1))
				passwords = append(passwords, pws)
			}
		}
	}
	return lines, passwords
}

// Two or three curl/wget/HTTPie commands on one line, glued by separators,
// JSON escapes and paired or unpaired quotes: every password is replaced, and
// a second pass changes nothing.
func TestRED_GluedCommandsLoseEveryPassword(t *testing.T) {
	for _, c := range [][2]int{{2, 0}, {2, 3}, {3, 0}, {3, 3}} {
		n := c[0]
		lines, passwords := gluedCommands(n, c[1])
		failed := 0
		for i, in := range lines {
			if n > 2 && (testing.Short() || raceEnabled) && i%67 != 0 {
				continue
			}
			out := RedactSecrets(in)
			for _, pw := range passwords[i] {
				if strings.Contains(out, pw) {
					if failed++; failed <= 20 {
						t.Errorf("password %s readable\n in =%s\n out=%s", pw, in, out)
					}
					break
				}
			}
			if again := RedactSecrets(out); again != out {
				t.Errorf("not stable\n once =%s\n twice=%s", out, again)
			}
		}
		t.Logf("%d commands: %d lines, %d with a readable password", n, len(lines), failed)
	}
}

// Reading every command word as its own command stays linear on lines built
// of nothing but command words: ten times the input may not cost much more
// than ten times the time.
func TestRED_GluedCommandWordsScaleLinearly(t *testing.T) {
	gens := map[string]func(n int) string{
		"quote glued":       func(n int) string { return strings.Repeat(`http -a u:p "`, n/13) },
		"escape glued":      func(n int) string { return strings.Repeat(`\nhttp -a u:p `, n/14) },
		"escape in string":  func(n int) string { return `"` + strings.Repeat(`\nhttp -a u:p `, n/14) },
		"blank glued":       func(n int) string { return strings.Repeat("http ", n/5) },
		"single quotes":     func(n int) string { return strings.Repeat("http 'a ", n/8) },
		"alternating":       func(n int) string { return strings.Repeat(`curl -u a:b ' curl -u a:b " `, n/28) },
		"planted passwords": func(n int) string { return strings.Repeat(`http -a user:Qz7KMafUn4qlp "`, n/28) },
	}
	base := 64 << 10
	if raceEnabled {
		base = 16 << 10
	}
	for name, gen := range gens {
		small, large := finalRedactTime(gen(base), 3), finalRedactTime(gen(10*base), 2)
		if small < time.Millisecond {
			small = time.Millisecond
		}
		if large > 25*small {
			t.Errorf("%s: super-linear: %d bytes %v, x10 %v", name, base, small, large)
		}
	}
}
