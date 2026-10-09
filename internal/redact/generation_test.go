package redact

import "testing"

func TestGeneration_ChangesOnRegistryMutation(t *testing.T) {
	ResetSecrets()
	t.Cleanup(ResetSecrets)
	g0 := Generation()
	RegisterSecret("generation-test-secret-value-0123456789")
	g1 := Generation()
	if g1 == g0 {
		t.Fatal("registering a new secret must change the generation")
	}
	RegisterSecret("generation-test-secret-value-0123456789")
	if Generation() != g1 {
		t.Fatal("re-registering a known secret must not change the generation")
	}
	RegisterSecret("short")
	if Generation() != g1 {
		t.Fatal("ignored (too short) values must not change the generation")
	}
	ResetSecrets()
	if Generation() == g1 {
		t.Fatal("resetting the registry must change the generation")
	}
}
