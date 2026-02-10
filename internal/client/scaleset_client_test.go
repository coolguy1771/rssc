/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package client

import (
	"context"
	"testing"

	"github.com/actions/scaleset"
)

func TestClientFactory_CreateClient_NoAuth(t *testing.T) {
	f := NewClientFactory(nil)
	ctx := context.Background()
	_, err := f.CreateClient(ctx, ClientConfig{
		GitHubConfigURL: "https://github.com/org/repo",
	})
	if err == nil {
		t.Fatal("expected error when neither GitHubApp nor PAT provided")
	}
	if err.Error() != "either GitHubApp or PAT must be provided" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestClientFactory_NewClientFactory_Options(t *testing.T) {
	f := NewClientFactory(nil, WithRateLimiter(10, 2))
	if f.GetRateLimiter() == nil {
		t.Error("expected rate limiter when option provided")
	}
}

func TestClientFactory_GetRateLimiter_DefaultNil(t *testing.T) {
	f := NewClientFactory(nil)
	if f.GetRateLimiter() != nil {
		t.Error("default rate limiter should be nil")
	}
}

func TestClientFactory_CreateClient_PAT(t *testing.T) {
	// CreateClient with PAT calls scaleset.NewClientWithPersonalAccessToken
	// which may validate URL/token; we only check it doesn't panic and
	// returns either client or validation error
	f := NewClientFactory(nil)
	ctx := context.Background()
	_, err := f.CreateClient(ctx, ClientConfig{
		GitHubConfigURL: "https://api.github.com",
		PAT:             &PATConfig{Token: "ghp_test"},
		SystemInfo:      scaleset.SystemInfo{},
	})
	// We expect either success (client created) or an error (e.g. invalid URL)
	if err != nil && err.Error() == "either GitHubApp or PAT must be provided" {
		t.Errorf("unexpected auth error: %v", err)
	}
}
