package skills

import "testing"

func TestValidateSkillName_Empty(t *testing.T) {
	if err := ValidateSkillName(""); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestValidateSkillName_PathSeparator(t *testing.T) {
	if err := ValidateSkillName("foo/bar"); err == nil {
		t.Fatal("expected error for path separator")
	}
	if err := ValidateSkillName("foo\\bar"); err == nil {
		t.Fatal("expected error for backslash")
	}
}

func TestValidateSkillName_Traversal(t *testing.T) {
	if err := ValidateSkillName("foo..bar"); err == nil {
		t.Fatal("expected error for '..' in name")
	}
}

func TestValidateSkillName_RelativePath(t *testing.T) {
	if err := ValidateSkillName("."); err == nil {
		t.Fatal("expected error for '.'")
	}
	if err := ValidateSkillName(".."); err == nil {
		t.Fatal("expected error for '..'")
	}
}

func TestValidateSkillName_Hidden(t *testing.T) {
	if err := ValidateSkillName(".hidden"); err == nil {
		t.Fatal("expected error for dot-prefixed name")
	}
}

func TestValidateSkillName_Valid(t *testing.T) {
	if err := ValidateSkillName("my-skill"); err != nil {
		t.Errorf("unexpected error for valid name: %v", err)
	}
	if err := ValidateSkillName("deploy_script"); err != nil {
		t.Errorf("unexpected error for valid name: %v", err)
	}
}
