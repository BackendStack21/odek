package loop

import (
	"strings"
	"testing"
)

// create accepts ids that revise's whole-plan validation rejects, so a plan
// created with such an id can never be revised afterwards.
func TestRED_CreateIDAcceptedButRevisePoisoned(t *testing.T) {
	s := NewPlanStore(12, 4000)
	if _, err := s.Execute("{\"verb\":\"create\",\"steps\":[{\"id\":\"a\\u00a0b\",\"title\":\"one\"},{\"id\":\"s2\",\"title\":\"two\"}]}"); err != nil {
		t.Skipf("create rejects the id (fine): %v", err)
	}
	_, err := s.Execute(`{"verb":"revise","reason":"add","operations":[{"kind":"add","steps":[{"id":"s3","title":"three"}]}]}`)
	if err != nil {
		t.Fatalf("create accepted id but revise of unrelated step now fails: %v", err)
	}
}

// create rejects every id shape revise would later reject.
func TestCreateRejectsIDsReviseRejects(t *testing.T) {
	for name, id := range map[string]string{
		"nbsp":    "a b",
		"ideo":    "a　b",
		"control": "a\x07b",
		"bracket": "a[b",
	} {
		s := NewPlanStore(12, 4000)
		_, err := s.Execute(`{"verb":"create","steps":[{"id":` + jsonString(id) + `,"title":"one"}]}`)
		if err == nil || !strings.Contains(err.Error(), "short token") {
			t.Errorf("%s: create err = %v, want id rejection", name, err)
		}
	}
	s := NewPlanStore(12, 4000)
	if _, err := s.Execute(`{"verb":"create","steps":[{"id":"ok-1","title":"one"}]}`); err != nil {
		t.Fatalf("plain id rejected: %v", err)
	}
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			b.WriteString(`\u`)
			const hex = "0123456789abcdef"
			b.WriteByte(hex[(r>>12)&0xf])
			b.WriteByte(hex[(r>>8)&0xf])
			b.WriteByte(hex[(r>>4)&0xf])
			b.WriteByte(hex[r&0xf])
			continue
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
