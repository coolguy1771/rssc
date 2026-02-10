/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package errors

import "strings"

// IsScaleSetRegistrationUnsupported reports whether the error indicates
// runner scale sets are not supported (e.g. personal account, not org/enterprise).
func IsScaleSetRegistrationUnsupported(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "StatusCode 404") &&
		strings.Contains(s, "registration-token")
}

// IsSessionConflict reports whether the error indicates an active session
// already exists (HTTP 409).
func IsSessionConflict(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return (strings.Contains(s, "status=409") || strings.Contains(s, "status=\"409\"")) &&
		strings.Contains(s, "already has an active session")
}
