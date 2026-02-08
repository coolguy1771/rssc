/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package constants

import "time"

const (
	// API group label/annotation prefixes and keys
	LabelRunnerName   = "scaleset.actions.github.com/runner-name"
	LabelRunnerState  = "scaleset.actions.github.com/runner-state"
	LabelAutoscaleSet = "scaleset.actions.github.com/autoscaleset"
	LabelRunnerGroup  = "scaleset.actions.github.com/runner-group"

	AnnotationJITConfig                  = "scaleset.actions.github.com/jit-config"
	AnnotationPodSpecHash                = "scaleset.actions.github.com/pod-spec-hash"
	AnnotationLastAutoscaleSetGeneration = "scaleset.actions.github.com/last-autoscaleset-generation"

	FinalizerName          = "scaleset.actions.github.com/finalizer"
	RunnerSetFinalizerName = "scaleset.actions.github.com/runnerset-finalizer"

	RunnerStateIdle = "idle"
	RunnerStateBusy = "busy"
)

// Requeue intervals
const (
	RequeueActiveInterval       = 2 * time.Minute
	RequeueErrorInterval        = 5 * time.Minute
	RequeueScaleSetIDWait       = 30 * time.Second
	SessionConflictRequeueAfter = 2 * time.Minute
	DeletionRequeueAfter        = time.Minute
)

// Scaler and cleanup timing
const (
	PendingPodCleanupThreshold = 5 * time.Minute
	CleanupTimeout             = 30 * time.Second
	DeleteGracePeriodSeconds   = 30
	DebounceWindow             = 2 * time.Second
	BatchMaxWorkers            = 20
)

// Default scaling limits when not specified on AutoscaleSet
const (
	DefaultMaxRunners = 10
	DefaultMinRunners = 0
)
