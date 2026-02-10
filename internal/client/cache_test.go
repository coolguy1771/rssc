/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package client

import (
	"context"
	"errors"
	"testing"

	"github.com/actions/scaleset"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNewCache(t *testing.T) {
	c := NewCache(nil)
	if c == nil {
		t.Fatal("NewCache returned nil")
	}
	if c.secretCache == nil || c.scaleSetCache == nil {
		t.Error("cache maps not initialized")
	}
}

func TestCache_GetPATConfig_MissingSecret(t *testing.T) {
	ctx := context.Background()
	fakeClient := fake.NewClientBuilder().Build()
	c := NewCache(fakeClient)
	key := types.NamespacedName{Namespace: "default", Name: "missing"}
	_, err := c.GetPATConfig(ctx, "default", key)
	if err == nil {
		t.Fatal("expected error for missing secret")
	}
}

func TestCache_GetPATConfig_SecretNoToken(t *testing.T) {
	ctx := context.Background()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pat"},
		Data:       map[string][]byte{"other": []byte("x")},
	}
	fakeClient := fake.NewClientBuilder().WithObjects(secret).Build()
	c := NewCache(fakeClient)
	key := types.NamespacedName{Namespace: "default", Name: "pat"}
	_, err := c.GetPATConfig(ctx, "default", key)
	if err == nil {
		t.Fatal("expected error when secret has no token key")
	}
}

func TestCache_GetPATConfig_Success(t *testing.T) {
	ctx := context.Background()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pat"},
		Data:       map[string][]byte{"token": []byte("ghp_abc")},
	}
	fakeClient := fake.NewClientBuilder().WithObjects(secret).Build()
	c := NewCache(fakeClient)
	key := types.NamespacedName{Namespace: "default", Name: "pat"}
	cfg, err := c.GetPATConfig(ctx, "default", key)
	if err != nil {
		t.Fatalf("GetPATConfig: %v", err)
	}
	if cfg.Token != "ghp_abc" {
		t.Errorf("token = %q, want ghp_abc", cfg.Token)
	}
	// Second call should hit cache
	cfg2, err := c.GetPATConfig(ctx, "default", key)
	if err != nil {
		t.Fatalf("second GetPATConfig: %v", err)
	}
	if cfg2.Token != cfg.Token {
		t.Error("cached config mismatch")
	}
}

func TestCache_GetPATConfig_KeyVariants(t *testing.T) {
	ctx := context.Background()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pat"},
		Data:       map[string][]byte{"pat": []byte("token1")},
	}
	fakeClient := fake.NewClientBuilder().WithObjects(secret).Build()
	c := NewCache(fakeClient)
	cfg, err := c.GetPATConfig(ctx, "default", types.NamespacedName{Name: "pat"})
	if err != nil {
		t.Fatalf("GetPATConfig: %v", err)
	}
	if cfg.Token != "token1" {
		t.Errorf("token = %q", cfg.Token)
	}
}

func TestCache_GetGitHubAppConfig_MissingSecret(t *testing.T) {
	ctx := context.Background()
	fakeClient := fake.NewClientBuilder().Build()
	c := NewCache(fakeClient)
	key := types.NamespacedName{Namespace: "default", Name: "missing"}
	_, err := c.GetGitHubAppConfig(ctx, "default", key, "client", 1)
	if err == nil {
		t.Fatal("expected error for missing secret")
	}
}

func TestCache_GetGitHubAppConfig_NoPrivateKey(t *testing.T) {
	ctx := context.Background()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "app"},
		Data:       map[string][]byte{"other": []byte("x")},
	}
	fakeClient := fake.NewClientBuilder().WithObjects(secret).Build()
	c := NewCache(fakeClient)
	key := types.NamespacedName{Namespace: "default", Name: "app"}
	_, err := c.GetGitHubAppConfig(ctx, "default", key, "cid", 1)
	if err == nil {
		t.Fatal("expected error when secret has no private key")
	}
}

func TestCache_GetGitHubAppConfig_Success(t *testing.T) {
	ctx := context.Background()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "app"},
		Data:       map[string][]byte{"privateKey": []byte("pem-content")},
	}
	fakeClient := fake.NewClientBuilder().WithObjects(secret).Build()
	c := NewCache(fakeClient)
	key := types.NamespacedName{Namespace: "default", Name: "app"}
	cfg, err := c.GetGitHubAppConfig(ctx, "default", key, "client-id", 2)
	if err != nil {
		t.Fatalf("GetGitHubAppConfig: %v", err)
	}
	if cfg.ClientID != "client-id" || cfg.InstallationID != 2 || cfg.PrivateKey != "pem-content" {
		t.Errorf("config mismatch: %+v", cfg)
	}
}

func TestCache_InvalidateSecret(t *testing.T) {
	c := NewCache(fake.NewClientBuilder().Build())
	key := types.NamespacedName{Namespace: "ns", Name: "secret"}
	c.InvalidateSecret(key)
	// No panic; next Get would miss cache
}

func TestCache_GetScaleSet_MissThenHit(t *testing.T) {
	c := NewCache(fake.NewClientBuilder().Build())
	callCount := 0
	getter := func() (*scaleset.RunnerScaleSet, error) {
		callCount++
		return &scaleset.RunnerScaleSet{ID: 42}, nil
	}
	s1, err := c.GetScaleSet(42, getter)
	if err != nil {
		t.Fatalf("GetScaleSet: %v", err)
	}
	if s1.ID != 42 {
		t.Errorf("ID = %d", s1.ID)
	}
	if callCount != 1 {
		t.Errorf("getter calls = %d, want 1", callCount)
	}
	s2, err := c.GetScaleSet(42, getter)
	if err != nil {
		t.Fatalf("second GetScaleSet: %v", err)
	}
	if s2.ID != 42 {
		t.Errorf("cached ID = %d", s2.ID)
	}
	if callCount != 1 {
		t.Errorf("getter calls = %d after cache hit, want 1", callCount)
	}
}

func TestCache_GetScaleSet_GetterError(t *testing.T) {
	c := NewCache(fake.NewClientBuilder().Build())
	getter := func() (*scaleset.RunnerScaleSet, error) {
		return nil, errGetterFailed
	}
	_, err := c.GetScaleSet(1, getter)
	if err != errGetterFailed {
		t.Errorf("expected getter error, got %v", err)
	}
}

func TestCache_InvalidateScaleSet(t *testing.T) {
	c := NewCache(fake.NewClientBuilder().Build())
	c.InvalidateScaleSet(99)
}

func TestCache_Cleanup(t *testing.T) {
	c := NewCache(fake.NewClientBuilder().Build())
	c.Cleanup()
}

var errGetterFailed = errors.New("getter failed")
