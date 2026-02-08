/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/actions/scaleset"
	scalesetv1alpha1 "github.com/coolguy1771/rssc/api/v1alpha1"
	"github.com/coolguy1771/rssc/internal/client"
	"github.com/coolguy1771/rssc/internal/metrics"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	FinalizerName = "scaleset.actions.github.com/finalizer"
)

// AutoscaleSetReconciler reconciles a AutoscaleSet object
type AutoscaleSetReconciler struct {
	k8sclient.Client
	Scheme        *runtime.Scheme
	ClientFactory *client.ClientFactory
	Recorder      record.EventRecorder
}

// +kubebuilder:rbac:groups=scaleset.actions.github.com,resources=autoscalesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scaleset.actions.github.com,resources=autoscalesets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scaleset.actions.github.com,resources=autoscalesets/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *AutoscaleSetReconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {
	startTime := time.Now()
	logger := log.FromContext(ctx)
	defer func() {
		duration := time.Since(startTime).Seconds()
		metrics.ReconciliationDuration.WithLabelValues(
			"autoscaleset",
			req.Namespace,
			req.Name,
		).Observe(duration)
	}()

	autoscaleSet := &scalesetv1alpha1.AutoscaleSet{}
	if err := r.Get(ctx, req.NamespacedName, autoscaleSet); err != nil {
		if errors.IsNotFound(err) {
			metrics.ReconciliationRate.WithLabelValues("autoscaleset", "not_found").Inc()
			return ctrl.Result{}, nil
		}
		metrics.ReconciliationRate.WithLabelValues("autoscaleset", "error").Inc()
		return ctrl.Result{}, err
	}

	if !autoscaleSet.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, autoscaleSet, logger)
	}

	if !controllerutil.ContainsFinalizer(autoscaleSet, FinalizerName) {
		controllerutil.AddFinalizer(autoscaleSet, FinalizerName)
		if err := r.Update(ctx, autoscaleSet); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	scalesetClient, err := r.getScalesetClient(ctx, autoscaleSet)
	if err != nil {
		return r.updateStatusError(
			ctx,
			autoscaleSet,
			"ClientCreationFailed",
			fmt.Sprintf("Failed to create scaleset client: %v", err),
			logger,
		)
	}

	runnerGroupID, err := r.getRunnerGroupID(
		ctx,
		scalesetClient,
		autoscaleSet.Spec.RunnerGroup,
	)
	if err != nil {
		return r.updateStatusError(
			ctx,
			autoscaleSet,
			"RunnerGroupNotFound",
			fmt.Sprintf("Failed to get runner group: %v", err),
			logger,
		)
	}

	if autoscaleSet.Status.ScaleSetID == nil {
		return r.createScaleSet(
			ctx,
			autoscaleSet,
			scalesetClient,
			runnerGroupID,
			logger,
		)
	}

	return r.updateScaleSet(
		ctx,
		autoscaleSet,
		scalesetClient,
		logger,
	)
}

func (r *AutoscaleSetReconciler) getScalesetClient(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
) (*scaleset.Client, error) {
	config := client.ClientConfig{
		GitHubConfigURL: autoscaleSet.Spec.GitHubConfigURL,
		SystemInfo: scaleset.SystemInfo{
			System:    "actions-runner-controller",
			Version:   "v1alpha1",
			CommitSHA: "unknown",
			Subsystem: "controller",
		},
	}

	if autoscaleSet.Spec.GitHubApp != nil {
		appConfig, err := r.ClientFactory.LoadGitHubAppFromSecret(
			ctx,
			autoscaleSet.Namespace,
			types.NamespacedName{
				Namespace: autoscaleSet.Spec.GitHubApp.PrivateKeySecretRef.Namespace,
				Name:      autoscaleSet.Spec.GitHubApp.PrivateKeySecretRef.Name,
			},
			autoscaleSet.Spec.GitHubApp.ClientID,
			autoscaleSet.Spec.GitHubApp.InstallationID,
		)
		if err != nil {
			return nil, err
		}
		config.GitHubApp = appConfig
	} else if autoscaleSet.Spec.PersonalAccessToken != nil {
		patConfig, err := r.ClientFactory.LoadPATFromSecret(
			ctx,
			autoscaleSet.Namespace,
			types.NamespacedName{
				Namespace: autoscaleSet.Spec.PersonalAccessToken.TokenSecretRef.Namespace,
				Name:      autoscaleSet.Spec.PersonalAccessToken.TokenSecretRef.Name,
			},
		)
		if err != nil {
			return nil, err
		}
		config.PAT = patConfig
	} else {
		return nil, fmt.Errorf(
			"either GitHubApp or PersonalAccessToken must be specified",
		)
	}

	scalesetClient, err := r.ClientFactory.CreateClient(ctx, config)
	if err != nil {
		return nil, err
	}

	if autoscaleSet.Status.ScaleSetID != nil {
		scalesetClient.SetSystemInfo(scaleset.SystemInfo{
			System:     "actions-runner-controller",
			Version:    "v1alpha1",
			CommitSHA:  "unknown",
			ScaleSetID: *autoscaleSet.Status.ScaleSetID,
			Subsystem:  "controller",
		})
	}

	return scalesetClient, nil
}

func (r *AutoscaleSetReconciler) getRunnerGroupID(
	ctx context.Context,
	scalesetClient *scaleset.Client,
	runnerGroupName string,
) (int, error) {
	if runnerGroupName == scaleset.DefaultRunnerGroup {
		return 1, nil
	}

	runnerGroup, err := scalesetClient.GetRunnerGroupByName(
		ctx,
		runnerGroupName,
	)
	if err != nil {
		return 0, err
	}

	return runnerGroup.ID, nil
}

func (r *AutoscaleSetReconciler) createScaleSet(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	scalesetClient *scaleset.Client,
	runnerGroupID int,
	logger logr.Logger,
) (ctrl.Result, error) {
	labels := r.buildLabels(autoscaleSet)

	logger.Info(
		"Creating scale set with labels",
		"scaleSetName", autoscaleSet.Spec.ScaleSetName,
		"labels", formatLabelsForLog(labels),
	)

	scaleSet := &scaleset.RunnerScaleSet{
		Name:          autoscaleSet.Spec.ScaleSetName,
		RunnerGroupID: runnerGroupID,
		Labels:        labels,
		RunnerSetting: scaleset.RunnerSetting{
			DisableUpdate: true,
		},
	}

	var created *scaleset.RunnerScaleSet
	err := client.RetryWithBackoff(ctx, client.DefaultRetryConfig, func() error {
		var retryErr error
		created, retryErr = scalesetClient.CreateRunnerScaleSet(ctx, scaleSet)
		if retryErr == nil && created != nil {
			logger.Info(
				"Scale set created by GitHub",
				"scaleSetID", created.ID,
				"labels", formatLabelsForLog(created.Labels),
			)
		}
		return retryErr
	})

	if err != nil {
		errorMsg := fmt.Sprintf("Failed to create scale set: %v", err)
		if strings.Contains(err.Error(), "StatusCode 404") &&
			strings.Contains(err.Error(), "registration-token") {
			errorMsg = fmt.Sprintf(
				"Failed to create scale set: %v. Note: Runner scale sets are only supported for GitHub organizations and enterprises, not personal user accounts. Please ensure your githubConfigURL points to an organization.",
				err,
			)
		}
		return r.updateStatusError(
			ctx,
			autoscaleSet,
			"ScaleSetCreationFailed",
			errorMsg,
			logger,
		)
	}

	if err := r.Get(ctx, types.NamespacedName{
		Namespace: autoscaleSet.Namespace,
		Name:      autoscaleSet.Name,
	}, autoscaleSet); err != nil {
		return ctrl.Result{}, err
	}

	autoscaleSet.Status.ScaleSetID = &created.ID
	autoscaleSet.Status.Phase = "Active"
	if created.Labels != nil {
		autoscaleSet.Status.Labels = make([]scalesetv1alpha1.ScaleSetLabel, len(created.Labels))
		for i, label := range created.Labels {
			autoscaleSet.Status.Labels[i] = scalesetv1alpha1.ScaleSetLabel{
				Type: label.Type,
				Name: label.Name,
			}
		}
	}
	meta.SetStatusCondition(&autoscaleSet.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "ScaleSetCreated",
		Message:            "Scale set created successfully",
		ObservedGeneration: autoscaleSet.Generation,
	})

	if err := r.Status().Update(ctx, autoscaleSet); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	metrics.ScaleSetOperations.WithLabelValues(
		autoscaleSet.Namespace,
		autoscaleSet.Name,
		"create",
		"success",
	).Inc()

	metrics.AutoscaleSetTotal.WithLabelValues(
		autoscaleSet.Namespace,
		autoscaleSet.Status.Phase,
	).Set(1)

	logger.Info(
		"Created scale set",
		"scaleSetID", created.ID,
		"labels", formatLabelsForLog(created.Labels),
	)

	r.Recorder.Event(
		autoscaleSet,
		corev1.EventTypeNormal,
		"ScaleSetCreated",
		fmt.Sprintf("Successfully created scale set %s with ID %d", autoscaleSet.Spec.ScaleSetName, created.ID),
	)

	return ctrl.Result{}, nil
}

func (r *AutoscaleSetReconciler) updateScaleSet(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	scalesetClient *scaleset.Client,
	logger logr.Logger,
) (ctrl.Result, error) {
	scaleSet, err := r.ClientFactory.GetCachedScaleSet(
		*autoscaleSet.Status.ScaleSetID,
		func() (*scaleset.RunnerScaleSet, error) {
			ss, getErr := scalesetClient.GetRunnerScaleSetByID(
				ctx,
				*autoscaleSet.Status.ScaleSetID,
			)
			if getErr == nil && ss != nil {
				logger.V(1).Info(
					"Retrieved scale set from GitHub",
					"scaleSetID", ss.ID,
					"labels", formatLabelsForLog(ss.Labels),
				)
			}
			return ss, getErr
		},
	)
	if err != nil {
		return r.updateStatusError(
			ctx,
			autoscaleSet,
			"ScaleSetNotFound",
			fmt.Sprintf("Failed to get scale set: %v", err),
			logger,
		)
	}

	if err := r.Get(ctx, types.NamespacedName{
		Namespace: autoscaleSet.Namespace,
		Name:      autoscaleSet.Name,
	}, autoscaleSet); err != nil {
		return ctrl.Result{}, err
	}

	// Check if labels need to be updated
	desiredLabels := r.buildLabels(autoscaleSet)

	labelsChanged := !labelsEqual(scaleSet.Labels, desiredLabels)
	if labelsChanged {
		logger.Info(
			"Labels changed, updating scale set",
			"scaleSetID", *autoscaleSet.Status.ScaleSetID,
			"currentLabels", formatLabelsForLog(scaleSet.Labels),
			"desiredLabels", formatLabelsForLog(desiredLabels),
		)
		updateScaleSet := &scaleset.RunnerScaleSet{
			Name:          autoscaleSet.Spec.ScaleSetName,
			RunnerGroupID: scaleSet.RunnerGroupID,
			Labels:        desiredLabels,
			RunnerSetting: scaleSet.RunnerSetting,
		}

		err := client.RetryWithBackoff(ctx, client.DefaultRetryConfig, func() error {
			updated, retryErr := scalesetClient.UpdateRunnerScaleSet(
				ctx,
				*autoscaleSet.Status.ScaleSetID,
				updateScaleSet,
			)
			if retryErr != nil {
				return retryErr
			}
			if updated != nil {
				logger.Info(
					"Scale set updated by GitHub",
					"scaleSetID", updated.ID,
					"labels", formatLabelsForLog(updated.Labels),
				)
			}
			scaleSet = updated
			return nil
		})

		if err != nil {
			logger.Error(
				err,
				"Failed to update scale set labels",
				"scaleSetID", *autoscaleSet.Status.ScaleSetID,
			)
			return r.updateStatusError(
				ctx,
				autoscaleSet,
				"ScaleSetUpdateFailed",
				fmt.Sprintf("Failed to update scale set labels: %v", err),
				logger,
			)
		}

		logger.Info(
			"Successfully updated scale set labels",
			"scaleSetID", *autoscaleSet.Status.ScaleSetID,
			"labels", formatLabelsForLog(scaleSet.Labels),
		)

		r.Recorder.Event(
			autoscaleSet,
			corev1.EventTypeNormal,
			"ScaleSetLabelsUpdated",
			fmt.Sprintf("Updated labels for scale set %d", *autoscaleSet.Status.ScaleSetID),
		)
	}

	if scaleSet.Labels != nil {
		autoscaleSet.Status.Labels = make([]scalesetv1alpha1.ScaleSetLabel, len(scaleSet.Labels))
		for i, label := range scaleSet.Labels {
			autoscaleSet.Status.Labels[i] = scalesetv1alpha1.ScaleSetLabel{
				Type: label.Type,
				Name: label.Name,
			}
		}
	}

	if scaleSet.Statistics != nil {
		autoscaleSet.Status.CurrentRunners = int32(
			scaleSet.Statistics.TotalRegisteredRunners,
		)
		autoscaleSet.Status.DesiredRunners = int32(
			scaleSet.Statistics.TotalAssignedJobs,
		)

		metrics.ScaleSetStatistics.WithLabelValues(
			autoscaleSet.Namespace,
			autoscaleSet.Name,
			"total_available_jobs",
		).Set(float64(scaleSet.Statistics.TotalAvailableJobs))

		metrics.ScaleSetStatistics.WithLabelValues(
			autoscaleSet.Namespace,
			autoscaleSet.Name,
			"total_acquired_jobs",
		).Set(float64(scaleSet.Statistics.TotalAcquiredJobs))

		metrics.ScaleSetStatistics.WithLabelValues(
			autoscaleSet.Namespace,
			autoscaleSet.Name,
			"total_assigned_jobs",
		).Set(float64(scaleSet.Statistics.TotalAssignedJobs))

		metrics.ScaleSetStatistics.WithLabelValues(
			autoscaleSet.Namespace,
			autoscaleSet.Name,
			"total_running_jobs",
		).Set(float64(scaleSet.Statistics.TotalRunningJobs))

		metrics.ScaleSetStatistics.WithLabelValues(
			autoscaleSet.Namespace,
			autoscaleSet.Name,
			"total_registered_runners",
		).Set(float64(scaleSet.Statistics.TotalRegisteredRunners))

		metrics.ScaleSetStatistics.WithLabelValues(
			autoscaleSet.Namespace,
			autoscaleSet.Name,
			"total_busy_runners",
		).Set(float64(scaleSet.Statistics.TotalBusyRunners))

		metrics.ScaleSetStatistics.WithLabelValues(
			autoscaleSet.Namespace,
			autoscaleSet.Name,
			"total_idle_runners",
		).Set(float64(scaleSet.Statistics.TotalIdleRunners))
	}

	autoscaleSet.Status.Phase = "Active"
	meta.SetStatusCondition(&autoscaleSet.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "ScaleSetActive",
		Message:            "Scale set is active",
		ObservedGeneration: autoscaleSet.Generation,
	})

	if err := r.Status().Update(ctx, autoscaleSet); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	if autoscaleSet.Spec.MinRunners != nil {
		metrics.ScaleSetLimits.WithLabelValues(
			autoscaleSet.Namespace,
			autoscaleSet.Name,
			"min_runners",
		).Set(float64(*autoscaleSet.Spec.MinRunners))
	}

	if autoscaleSet.Spec.MaxRunners != nil {
		metrics.ScaleSetLimits.WithLabelValues(
			autoscaleSet.Namespace,
			autoscaleSet.Name,
			"max_runners",
		).Set(float64(*autoscaleSet.Spec.MaxRunners))
	}

	metrics.AutoscaleSetTotal.WithLabelValues(
		autoscaleSet.Namespace,
		autoscaleSet.Status.Phase,
	).Set(1)

	metrics.ReconciliationRate.WithLabelValues("autoscaleset", "success").Inc()
	if autoscaleSet.Status.Phase == "Active" &&
		meta.IsStatusConditionTrue(
			autoscaleSet.Status.Conditions,
			"Ready",
		) {
		return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil
	}
	return ctrl.Result{}, nil
}

func (r *AutoscaleSetReconciler) handleDeletion(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	logger logr.Logger,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(autoscaleSet, FinalizerName) {
		return ctrl.Result{}, nil
	}

	if autoscaleSet.Status.ScaleSetID != nil {
		scalesetClient, err := r.getScalesetClient(ctx, autoscaleSet)
		if err != nil {
			logger.Error(
				err,
				"Failed to create client for scale set deletion",
				"scaleSetID", *autoscaleSet.Status.ScaleSetID,
			)
			return ctrl.Result{RequeueAfter: time.Minute}, err
		}

		err = client.RetryWithBackoff(ctx, client.DefaultRetryConfig, func() error {
			return scalesetClient.DeleteRunnerScaleSet(
				ctx,
				*autoscaleSet.Status.ScaleSetID,
			)
		})
		if err != nil {
			logger.Error(
				err,
				"Failed to delete scale set",
				"scaleSetID", *autoscaleSet.Status.ScaleSetID,
			)
			return ctrl.Result{}, err
		}

		logger.Info(
			"Deleted scale set",
			"scaleSetID", *autoscaleSet.Status.ScaleSetID,
		)
		r.Recorder.Event(
			autoscaleSet,
			corev1.EventTypeNormal,
			"ScaleSetDeleted",
			fmt.Sprintf("Successfully deleted scale set with ID %d", *autoscaleSet.Status.ScaleSetID),
		)
	}

	controllerutil.RemoveFinalizer(autoscaleSet, FinalizerName)
	if err := r.Update(ctx, autoscaleSet); err != nil {
		return ctrl.Result{}, err
	}

	metrics.ScaleSetOperations.WithLabelValues(
		autoscaleSet.Namespace,
		autoscaleSet.Name,
		"delete",
		"success",
	).Inc()

	metrics.AutoscaleSetTotal.WithLabelValues(
		autoscaleSet.Namespace,
		autoscaleSet.Status.Phase,
	).Set(0)

	return ctrl.Result{}, nil
}

func (r *AutoscaleSetReconciler) updateStatusError(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	reason, message string,
	logger logr.Logger,
) (ctrl.Result, error) {
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: autoscaleSet.Namespace,
		Name:      autoscaleSet.Name,
	}, autoscaleSet); err != nil {
		return ctrl.Result{}, err
	}

	autoscaleSet.Status.Phase = "Error"
	meta.SetStatusCondition(&autoscaleSet.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: autoscaleSet.Generation,
	})

	if err := r.Status().Update(ctx, autoscaleSet); err != nil {
		if errors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	metrics.ScaleSetOperations.WithLabelValues(
		autoscaleSet.Namespace,
		autoscaleSet.Name,
		"reconcile",
		"error",
	).Inc()

	metrics.AutoscaleSetTotal.WithLabelValues(
		autoscaleSet.Namespace,
		autoscaleSet.Status.Phase,
	).Set(1)

	logger.Error(nil, message, "reason", reason)

	r.Recorder.Event(
		autoscaleSet,
		corev1.EventTypeWarning,
		reason,
		message,
	)

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *AutoscaleSetReconciler) buildLabels(
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
) []scaleset.Label {
	labels := make([]scaleset.Label, 0, len(autoscaleSet.Spec.Labels)+1)
	hasSelfHosted := false

	for _, l := range autoscaleSet.Spec.Labels {
		label := scaleset.Label{
			Type: l.Type,
			Name: l.Name,
		}
		if label.Type == "" {
			label.Type = "System"
		}
		if label.Name == "self-hosted" {
			hasSelfHosted = true
		}
		labels = append(labels, label)
	}

	if !hasSelfHosted {
		labels = append([]scaleset.Label{
			{
				Type: "System",
				Name: "self-hosted",
			},
		}, labels...)
	}

	if len(labels) == 1 && labels[0].Name == "self-hosted" {
		labels = append(labels, scaleset.Label{
			Type: "System",
			Name: autoscaleSet.Spec.ScaleSetName,
		})
	}

	return labels
}

func labelsEqual(a, b []scaleset.Label) bool {
	// Normalize label types to lowercase for comparison (GitHub returns lowercase)
	normalizeLabel := func(l scaleset.Label) string {
		return fmt.Sprintf("%s:%s", strings.ToLower(l.Type), l.Name)
	}

	// Build sets of normalized labels, excluding "self-hosted" from comparison
	// GitHub may automatically add/filter "self-hosted" so we don't compare it
	buildLabelSet := func(labels []scaleset.Label) map[string]bool {
		labelSet := make(map[string]bool)
		for _, label := range labels {
			normalized := normalizeLabel(label)
			// Skip "self-hosted" in comparison as GitHub may handle it automatically
			if !strings.HasSuffix(normalized, ":self-hosted") {
				labelSet[normalized] = true
			}
		}
		return labelSet
	}

	aSet := buildLabelSet(a)
	bSet := buildLabelSet(b)

	if len(aSet) != len(bSet) {
		return false
	}

	for key := range aSet {
		if !bSet[key] {
			return false
		}
	}

	return true
}

func formatLabelsForLog(labels []scaleset.Label) string {
	if len(labels) == 0 {
		return "[]"
	}
	parts := make([]string, len(labels))
	for i, label := range labels {
		parts[i] = fmt.Sprintf("%s:%s", label.Type, label.Name)
	}
	return fmt.Sprintf("[%s]", strings.Join(parts, ", "))
}

func (r *AutoscaleSetReconciler) mapSecretToAutoscaleSets(
	ctx context.Context,
	obj k8sclient.Object,
) []reconcile.Request {
	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}
	var list scalesetv1alpha1.AutoscaleSetList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		as := &list.Items[i]
		if r.autoscaleSetReferencesSecret(as, secret) {
			reqs = append(reqs, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: as.Namespace,
					Name:      as.Name,
				},
			})
		}
	}
	return reqs
}

func (r *AutoscaleSetReconciler) autoscaleSetReferencesSecret(
	as *scalesetv1alpha1.AutoscaleSet,
	secret *corev1.Secret,
) bool {
	refMatches := func(ref scalesetv1alpha1.SecretRef) bool {
		ns := ref.Namespace
		if ns == "" {
			ns = as.Namespace
		}
		return ns == secret.Namespace && ref.Name == secret.Name
	}
	if as.Spec.GitHubApp != nil && refMatches(as.Spec.GitHubApp.PrivateKeySecretRef) {
		return true
	}
	if as.Spec.PersonalAccessToken != nil &&
		refMatches(as.Spec.PersonalAccessToken.TokenSecretRef) {
		return true
	}
	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *AutoscaleSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&scalesetv1alpha1.AutoscaleSet{}).
		Named("autoscaleset").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 10,
		}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.mapSecretToAutoscaleSets),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}
