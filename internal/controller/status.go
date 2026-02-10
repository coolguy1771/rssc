/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package controller

import (
	"context"
	"errors"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
)

// ErrStatusConflict is returned when a status update fails due to resource version conflict.
var ErrStatusConflict = errors.New("status update conflict")

// RefetchAndUpdateStatus refetches the object, runs applyErrorCondition to set error state,
// then calls statusUpdate. Returns ErrStatusConflict on conflict so callers can requeue.
func RefetchAndUpdateStatus(
	ctx context.Context,
	c k8sclient.Client,
	key k8sclient.ObjectKey,
	obj k8sclient.Object,
	applyErrorCondition func(),
	statusUpdate func(context.Context, k8sclient.Object) error,
) error {
	if err := c.Get(ctx, key, obj); err != nil {
		return err
	}
	applyErrorCondition()
	if err := statusUpdate(ctx, obj); err != nil {
		if k8serrors.IsConflict(err) {
			return ErrStatusConflict
		}
		return err
	}
	return nil
}
