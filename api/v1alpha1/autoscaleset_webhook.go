package v1alpha1

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

var autoscalesetlog = logf.Log.WithName("autoscaleset-resource")

var _ admission.Defaulter[*AutoscaleSet] = &AutoscaleSet{}
var _ admission.Validator[*AutoscaleSet] = &AutoscaleSet{}

func (r *AutoscaleSet) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, r).
		WithDefaulter(r).
		WithValidator(r).
		Complete()
}

func (r *AutoscaleSet) Default(ctx context.Context, obj *AutoscaleSet) error {
	autoscalesetlog.Info("default", "name", obj.Name)

	if obj.Spec.RunnerGroup == "" {
		obj.Spec.RunnerGroup = "default"
	}

	if obj.Spec.MinRunners == nil {
		zero := int32(0)
		obj.Spec.MinRunners = &zero
	}

	if obj.Spec.MaxRunners == nil {
		ten := int32(10)
		obj.Spec.MaxRunners = &ten
	}

	for i := range obj.Spec.Labels {
		if obj.Spec.Labels[i].Type == "" {
			obj.Spec.Labels[i].Type = "System"
		}
	}

	return nil
}

func (r *AutoscaleSet) ValidateCreate(ctx context.Context, obj *AutoscaleSet) (admission.Warnings, error) {
	autoscalesetlog.Info("validate create", "name", obj.Name)
	return nil, obj.validate()
}

func (r *AutoscaleSet) ValidateUpdate(ctx context.Context, oldObj, newObj *AutoscaleSet) (admission.Warnings, error) {
	autoscalesetlog.Info("validate update", "name", newObj.Name)
	return nil, newObj.validateUpdate(oldObj)
}

func (r *AutoscaleSet) ValidateDelete(ctx context.Context, obj *AutoscaleSet) (admission.Warnings, error) {
	autoscalesetlog.Info("validate delete", "name", obj.Name)
	return nil, nil
}

func (r *AutoscaleSet) validate() error {
	if r.Spec.GitHubConfigURL == "" {
		return fmt.Errorf("githubConfigURL is required")
	}

	if r.Spec.RunnerImage == "" {
		return fmt.Errorf("runnerImage is required")
	}

	if r.Spec.GitHubApp == nil && r.Spec.PersonalAccessToken == nil {
		return fmt.Errorf("either githubApp or personalAccessToken must be specified")
	}

	if r.Spec.GitHubApp != nil && r.Spec.PersonalAccessToken != nil {
		return fmt.Errorf("cannot specify both githubApp and personalAccessToken")
	}

	if r.Spec.MinRunners != nil && r.Spec.MaxRunners != nil {
		if *r.Spec.MinRunners > *r.Spec.MaxRunners {
			return fmt.Errorf("minRunners (%d) cannot be greater than maxRunners (%d)", *r.Spec.MinRunners, *r.Spec.MaxRunners)
		}
	}

	return nil
}

func (r *AutoscaleSet) validateUpdate(old *AutoscaleSet) error {
	if err := r.validate(); err != nil {
		return err
	}

	if old.Status.ScaleSetID != nil && r.Spec.GitHubConfigURL != old.Spec.GitHubConfigURL {
		return fmt.Errorf("cannot change githubConfigURL after scale set is created")
	}

	return nil
}
