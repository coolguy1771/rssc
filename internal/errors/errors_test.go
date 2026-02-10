/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package errors

import (
	"errors"
	"testing"
)

func TestIsScaleSetRegistrationUnsupported(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"match", errors.New("StatusCode 404 and registration-token"), true},
		{"no 404", errors.New("registration-token only"), false},
		{"no registration-token", errors.New("StatusCode 404 only"), false},
		{"other", errors.New("some other error"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsScaleSetRegistrationUnsupported(tt.err)
			if got != tt.want {
				t.Errorf("IsScaleSetRegistrationUnsupported() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsSessionConflict(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"status=409 and session", errors.New("status=409 already has an active session"), true},
		{"status=409 quoted", errors.New("status=\"409\" already has an active session"), true},
		{"no session text", errors.New("status=409"), false},
		{"no 409", errors.New("already has an active session"), false},
		{"other", errors.New("some other error"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsSessionConflict(tt.err)
			if got != tt.want {
				t.Errorf("IsSessionConflict() = %v, want %v", got, tt.want)
			}
		})
	}
}
