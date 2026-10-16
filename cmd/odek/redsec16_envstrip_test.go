package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
)

func TestRED_BackgroundSettingsStripSecretsByDefault(t *testing.T) {
	var resolved config.ResolvedConfig
	if !backgroundSettingsFromResolved(resolved).StripChildSecretEnv {
		t.Fatal("background jobs inherit secrets.env names when strip_secrets_env_children is unset")
	}
	off := false
	resolved.Dangerous = danger.DangerousConfig{StripSecretsEnvChildren: &off}
	if backgroundSettingsFromResolved(resolved).StripChildSecretEnv {
		t.Fatal("explicit strip_secrets_env_children: false must keep inheritance")
	}
}
