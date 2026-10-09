package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const nSkills = 200

func mkSkillsDir(b testing.TB) string {
	dir := b.(interface{ TempDir() string }).TempDir()
	words := strings.Fields("alpha bravo charlie delta echo foxtrot golf hotel india juliet kilo lima mike november oscar papa quebec romeo sierra tango uniform victor whiskey xray yankee zulu build deploy test review config search")
	for i := 0; i < nSkills; i++ {
		d := filepath.Join(dir, fmt.Sprintf("skill-%03d", i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			b.Fatal(err)
		}
		var desc []string
		for j := 0; j < 40; j++ {
			desc = append(desc, words[(i*7+j*3)%len(words)])
		}
		content := fmt.Sprintf("---\nname: skill-%03d\ndescription: Use when %s\nodek:\n  trigger:\n    topic: %s, %s\n    action: %s, %s\n---\n\nBody text for skill %d. "+strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit. ", 36)+"\n",
			i, strings.Join(desc, " "),
			words[i%len(words)], words[(i+5)%len(words)],
			words[(i+1)%len(words)], words[(i+9)%len(words)], i)
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return dir
}

func BenchmarkNewSkillManager200(b *testing.B) {
	user := mkSkillsDir(b)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = NewSkillManager(user, filepath.Join(b.TempDir(), "none"))
	}
}

func BenchmarkMatchLazy200(b *testing.B) {
	user := mkSkillsDir(b)
	sm := NewSkillManager(user, filepath.Join(b.TempDir(), "none"))
	msg := "please build the release and then run the deploy tests for the config search module"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sm.MatchLazySkills(msg, 5)
	}
}

func BenchmarkScoredMatch200(b *testing.B) {
	user := mkSkillsDir(b)
	sm := NewSkillManager(user, filepath.Join(b.TempDir(), "none"))
	msg := "please build the release and then run the deploy tests for the config search module"
	scored := sm.ScoredMatcher
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = scored.MatchSkills(msg, 5)
	}
}

func BenchmarkVectorBuild200(b *testing.B) {
	user := mkSkillsDir(b)
	sm := NewSkillManager(user, filepath.Join(b.TempDir(), "none"))
	lazy := sm.Result.Lazy
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewVectorMatcherWithConfig(lazy, DefaultMatcherConfig, nil)
	}
}

func BenchmarkScanOnly200(b *testing.B) {
	user := mkSkillsDir(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ScanDirs("", user, nil)
	}
}
