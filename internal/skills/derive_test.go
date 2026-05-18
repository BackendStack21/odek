package skills

import (
	"strings"
	"testing"
)

func TestDeriveKeywords_Basic(t *testing.T) {
	body := `## Overview

This guide helps you build Docker containers with caching.

## Step-by-Step

1. Write a Dockerfile
2. Run docker build with cache-from
3. Tag the image
4. Push to registry

## Common Pitfalls

- Forgetting --cache-from flag
- Not using .dockerignore

## Verification

Run docker build --cache-from and check the output.`
	topics, actions := DeriveKeywords(body)
	if len(topics) == 0 {
		t.Error("expected at least some topic keywords")
	}
	if len(actions) == 0 {
		t.Error("expected at least some action keywords")
	}

	t.Logf("Topics: %v", topics)
	t.Logf("Actions: %v", actions)
}

func TestDeriveKeywords_ShortBody(t *testing.T) {
	body := `## Overview
Simple test`
	topics, actions := DeriveKeywords(body)
	if len(topics) == 0 {
		t.Error("expected topics even for short body")
	}
	// Actions may be empty for such a short body
	_ = actions
}

func TestDeriveKeywords_Empty(t *testing.T) {
	topics, actions := DeriveKeywords("")
	if len(topics) != 0 {
		t.Error("expected empty topics for empty body")
	}
	if len(actions) != 0 {
		t.Error("expected empty actions for empty body")
	}
}

func TestDeriveKeywords_ActionVerbs(t *testing.T) {
	body := `## Building and Deploying

To build the project, run the build command.
To deploy, run the deploy command.
Testing is done with go test.`
	_, actions := DeriveKeywords(body)
	hasAction := false
	for _, a := range actions {
		if strings.Contains(a, "build") || strings.Contains(a, "deploy") || strings.Contains(a, "testing") {
			hasAction = true
		}
	}
	if !hasAction {
		t.Errorf("expected action-related keywords, got %v", actions)
	}
}

func TestDeriveKeywords_StopwordsFiltered(t *testing.T) {
	body := `The the the and and and but but for for docker container build`
	topics, _ := DeriveKeywords(body)
	for _, w := range topics {
		if w == "the" || w == "and" || w == "but" || w == "for" {
			t.Errorf("stopword %q found in keywords", w)
		}
	}
}

func TestIsStopword(t *testing.T) {
	if !IsStopword("the") {
		t.Error("'the' should be a stopword")
	}
	if !IsStopword("and") {
		t.Error("'and' should be a stopword")
	}
	if IsStopword("docker") {
		t.Error("'docker' should not be a stopword")
	}
	if IsStopword("build") {
		t.Error("'build' should not be a stopword")
	}
}
