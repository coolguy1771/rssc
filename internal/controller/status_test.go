/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package controller

import (
	"context"
	"errors"
	"testing"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	scalesetv1alpha1 "github.com/coolguy1771/rssc/api/v1alpha1"
)

func TestRefetchAndUpdateStatus_Success(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := scalesetv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	obj := &scalesetv1alpha1.AutoscaleSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "as"},
		Status:     scalesetv1alpha1.AutoscaleSetStatus{},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(obj).WithObjects(obj).Build()
	ctx := context.Background()
	key := client.ObjectKeyFromObject(obj)
	applied := false
	err := RefetchAndUpdateStatus(ctx, c, key, obj,
		func() { applied = true },
		func(_ context.Context, o client.Object) error {
			return c.Status().Update(ctx, o)
		},
	)
	if err != nil {
		t.Fatalf("RefetchAndUpdateStatus: %v", err)
	}
	if !applied {
		t.Error("applyErrorCondition was not called")
	}
}

func TestRefetchAndUpdateStatus_GetNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := scalesetv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	ctx := context.Background()
	obj := &scalesetv1alpha1.AutoscaleSet{}
	key := client.ObjectKey{Namespace: "default", Name: "missing"}
	err := RefetchAndUpdateStatus(ctx, c, key, obj,
		func() {},
		func(_ context.Context, o client.Object) error {
			return c.Status().Update(ctx, o)
		},
	)
	if err == nil {
		t.Fatal("expected error for missing object")
	}
	if !k8serrors.IsNotFound(err) {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestRefetchAndUpdateStatus_Conflict(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := scalesetv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	obj := &scalesetv1alpha1.AutoscaleSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "as"},
		Status:     scalesetv1alpha1.AutoscaleSetStatus{},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(obj).WithObjects(obj).Build()
	ctx := context.Background()
	key := client.ObjectKeyFromObject(obj)
	err := RefetchAndUpdateStatus(ctx, c, key, obj,
		func() {},
		func(_ context.Context, _ client.Object) error {
			return k8serrors.NewConflict(schema.GroupResource{}, "as", nil)
		},
	)
	if err == nil {
		t.Fatal("expected error on conflict")
	}
	if !errors.Is(err, ErrStatusConflict) {
		t.Errorf("expected ErrStatusConflict, got %v", err)
	}
}
