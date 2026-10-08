package config

import "testing"

func TestValidRiskClass_AcceptsNetworkUpload(t *testing.T) {
	if !validRiskClass("network_upload") {
		t.Error("network_upload must be accepted as a profile max_risk")
	}
	profiles := resolveProfiles(map[string]ProfileConfig{"p": {MaxRisk: "network_upload"}})
	if _, ok := profiles["p"]; !ok {
		t.Error("a profile capped at network_upload must not be dropped")
	}
}
