package config

import "testing"

// RED: the layer merge (global file over project file) must carry the
// operator-only verify section through to the merged file config; an
// omitted field silently disabled verification end to end.
func TestOverlayFile_CarriesVerify(t *testing.T) {
	base := &FileConfig{}
	override := FileConfig{
		Verify: &VerifyFileConfig{
			Enabled:   boolPtr(true),
			Mode:      "hint",
			Model:     "glm-5.3-flash",
			MaxCycles: intPtr(3),
		},
	}
	merged := overlayFile(*base, override)
	if merged.Verify == nil {
		t.Fatal("verify section dropped by layer merge — verification would silently stay disabled")
	}
	if !*merged.Verify.Enabled || merged.Verify.Model != "glm-5.3-flash" {
		t.Fatalf("merged verify = %+v", merged.Verify)
	}
}

func TestOverlayFile_ExplicitOffWins(t *testing.T) {
	base := &FileConfig{Verify: &VerifyFileConfig{Enabled: boolPtr(true)}}
	override := FileConfig{Verify: &VerifyFileConfig{Enabled: boolPtr(false)}}
	merged := overlayFile(*base, override)
	if merged.Verify == nil || *merged.Verify.Enabled {
		t.Fatalf("higher-layer explicit off must win: %+v", merged.Verify)
	}
}
