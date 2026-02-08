package v1alpha1

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var runnersetlog = logf.Log.WithName("runnerset-resource")

var _ admission.Defaulter[*RunnerSet] = &RunnerSet{}
var _ admission.Validator[*RunnerSet] = &RunnerSet{}

func (r *RunnerSet) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, r).
		WithDefaulter(r).
		WithValidator(r).
		Complete()
}

func (r *RunnerSet) Default(ctx context.Context, obj *RunnerSet) error {
	runnersetlog.Info("default", "name", obj.Name)

	if obj.Spec.AutoscaleSetRef.Namespace == "" {
		obj.Spec.AutoscaleSetRef.Namespace = obj.Namespace
	}

	return nil
}

func (r *RunnerSet) ValidateCreate(ctx context.Context, obj *RunnerSet) (admission.Warnings, error) {
	runnersetlog.Info("validate create", "name", obj.Name)
	return nil, obj.validate()
}

func (r *RunnerSet) ValidateUpdate(ctx context.Context, oldObj, newObj *RunnerSet) (admission.Warnings, error) {
	runnersetlog.Info("validate update", "name", newObj.Name)
	return nil, newObj.validateUpdate(oldObj)
}

func (r *RunnerSet) ValidateDelete(ctx context.Context, obj *RunnerSet) (admission.Warnings, error) {
	runnersetlog.Info("validate delete", "name", obj.Name)
	return nil, nil
}

func (r *RunnerSet) validate() error {
	if r.Spec.AutoscaleSetRef.Name == "" {
		return fmt.Errorf("autoscaleSetRef.name is required")
	}

	if r.Spec.RunnerGroup == "" {
		return fmt.Errorf("runnerGroup is required")
	}

	return nil
}

func (r *RunnerSet) validateUpdate(old *RunnerSet) error {
	if err := r.validate(); err != nil {
		return err
	}

	if r.Spec.AutoscaleSetRef.Name != old.Spec.AutoscaleSetRef.Name ||
		r.Spec.AutoscaleSetRef.Namespace != old.Spec.AutoscaleSetRef.Namespace {
		return fmt.Errorf("autoscaleSetRef cannot be changed after creation")
	}

	if r.Spec.RunnerGroup != old.Spec.RunnerGroup {
		return fmt.Errorf("runnerGroup cannot be changed after creation")
	}

	return nil
}
