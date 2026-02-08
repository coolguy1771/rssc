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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/actions/scaleset"
	scalesetv1alpha1 "github.com/coolguy1771/rssc/api/v1alpha1"
	"github.com/coolguy1771/rssc/internal/client"
	"github.com/coolguy1771/rssc/internal/constants"
	rsscerrors "github.com/coolguy1771/rssc/internal/errors"
	"github.com/coolguy1771/rssc/internal/metrics"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
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

// AutoscaleSetReconciler reconciles a AutoscaleSet object
type AutoscaleSetReconciler struct {
	k8sclient.Client
	Scheme        *runtime.Scheme
	ClientFactory *client.ClientFactory
	Recorder      events.EventRecorder
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
	logger.Info("Reconciling AutoscaleSet", "namespace", req.Namespace, "name", req.Name)
	defer r.recordReconcileDuration("autoscaleset", req.Namespace, req.Name, startTime)

	autoscaleSet := &scalesetv1alpha1.AutoscaleSet{}
	if err := r.Get(ctx, req.NamespacedName, autoscaleSet); err != nil {
		if k8serrors.IsNotFound(err) {
			metrics.ReconciliationRate.WithLabelValues("autoscaleset", "not_found").Inc()
			return ctrl.Result{}, nil
		}
		metrics.ReconciliationRate.WithLabelValues("autoscaleset", "error").Inc()
		return ctrl.Result{}, err
	}

	if !autoscaleSet.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, autoscaleSet, logger)
	}

	err, done := r.ensureFinalizer(ctx, autoscaleSet)
	if done {
		return ctrl.Result{}, err
	}

	return r.reconcileActive(ctx, autoscaleSet, logger)
}

func (r *AutoscaleSetReconciler) recordReconcileDuration(kind, namespace, name string, startTime time.Time) {
	metrics.ReconciliationDuration.WithLabelValues(kind, namespace, name).Observe(time.Since(startTime).Seconds())
}

func (r *AutoscaleSetReconciler) ensureFinalizer(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
) (error, bool) {
	if controllerutil.ContainsFinalizer(autoscaleSet, constants.FinalizerName) {
		return nil, false
	}
	controllerutil.AddFinalizer(autoscaleSet, constants.FinalizerName)
	if err := r.Update(ctx, autoscaleSet); err != nil {
		return err, true
	}
	return nil, true
}

func (r *AutoscaleSetReconciler) reconcileActive(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	logger logr.Logger,
) (ctrl.Result, error) {
	scalesetClient, err := r.ClientFactory.CreateClientForAutoscaleSet(ctx, autoscaleSet, client.SubsystemController)
	if err != nil {
		return r.updateStatusError(ctx, autoscaleSet, "ClientCreationFailed",
			fmt.Sprintf("Failed to create scaleset client: %v", err), logger)
	}

	runnerGroupID, err := r.getRunnerGroupID(ctx, scalesetClient, autoscaleSet.Spec.RunnerGroup)
	if err != nil {
		return r.updateStatusError(ctx, autoscaleSet, "RunnerGroupNotFound",
			fmt.Sprintf("Failed to get runner group: %v", err), logger)
	}

	if autoscaleSet.Status.ScaleSetID == nil {
		return r.createScaleSet(ctx, autoscaleSet, scalesetClient, runnerGroupID, logger)
	}
	return r.updateScaleSet(ctx, autoscaleSet, scalesetClient, logger)
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
	logger.Info("Creating scale set with labels",
		"scaleSetName", autoscaleSet.Spec.ScaleSetName,
		"labels", formatLabelsForLog(labels),
	)

	created, err := r.createScaleSetInGitHub(ctx, autoscaleSet, scalesetClient, runnerGroupID, labels, logger)
	if err != nil {
		errorMsg := fmt.Sprintf("Failed to create scale set: %v", err)
		if rsscerrors.IsScaleSetRegistrationUnsupported(err) {
			errorMsg = fmt.Sprintf(
				"Failed to create scale set: %v. Note: Runner scale sets are only supported for GitHub organizations and enterprises, not personal user accounts. Please ensure your githubConfigURL points to an organization.",
				err,
			)
		}
		return r.updateStatusError(ctx, autoscaleSet, "ScaleSetCreationFailed", errorMsg, logger)
	}

	if err := r.Get(ctx, types.NamespacedName{
		Namespace: autoscaleSet.Namespace,
		Name:      autoscaleSet.Name,
	}, autoscaleSet); err != nil {
		return ctrl.Result{}, err
	}

	r.setCreateScaleSetStatus(autoscaleSet, created)
	if err := r.Status().Update(ctx, autoscaleSet); err != nil {
		if k8serrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	metrics.ScaleSetOperations.WithLabelValues(
		autoscaleSet.Namespace, autoscaleSet.Name, "create", "success",
	).Inc()
	metrics.AutoscaleSetTotal.WithLabelValues(autoscaleSet.Namespace, autoscaleSet.Status.Phase).Set(1)
	logger.Info("Created scale set", "scaleSetID", created.ID, "labels", formatLabelsForLog(created.Labels))
	r.Recorder.Eventf(autoscaleSet, nil, corev1.EventTypeNormal, "ScaleSetCreated", "ScaleSetCreated",
		"Successfully created scale set %s with ID %d", autoscaleSet.Spec.ScaleSetName, created.ID,
	)
	return ctrl.Result{}, nil
}

func (r *AutoscaleSetReconciler) createScaleSetInGitHub(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	scalesetClient *scaleset.Client,
	runnerGroupID int,
	labels []scaleset.Label,
	logger logr.Logger,
) (*scaleset.RunnerScaleSet, error) {
	scaleSet := &scaleset.RunnerScaleSet{
		Name:          autoscaleSet.Spec.ScaleSetName,
		RunnerGroupID: runnerGroupID,
		Labels:        labels,
		RunnerSetting: scaleset.RunnerSetting{DisableUpdate: true},
	}
	var created *scaleset.RunnerScaleSet
	err := client.RetryWithBackoff(ctx, client.DefaultRetryConfig, func() error {
		var retryErr error
		created, retryErr = scalesetClient.CreateRunnerScaleSet(ctx, scaleSet)
		if retryErr == nil && created != nil {
			logger.Info("Scale set created by GitHub", "scaleSetID", created.ID, "labels", formatLabelsForLog(created.Labels))
		}
		return retryErr
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

func (r *AutoscaleSetReconciler) setCreateScaleSetStatus(
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	created *scaleset.RunnerScaleSet,
) {
	autoscaleSet.Status.ScaleSetID = &created.ID
	autoscaleSet.Status.Phase = constants.PhaseActive
	if created.Labels != nil {
		autoscaleSet.Status.Labels = make([]scalesetv1alpha1.ScaleSetLabel, len(created.Labels))
		for i, label := range created.Labels {
			autoscaleSet.Status.Labels[i] = scalesetv1alpha1.ScaleSetLabel{Type: label.Type, Name: label.Name}
		}
	}
	meta.SetStatusCondition(&autoscaleSet.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, Reason: "ScaleSetCreated",
		Message: "Scale set created successfully", ObservedGeneration: autoscaleSet.Generation,
	})
}

func (r *AutoscaleSetReconciler) updateScaleSet(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	scalesetClient *scaleset.Client,
	logger logr.Logger,
) (ctrl.Result, error) {
	scaleSet, err := r.getScaleSetForUpdate(ctx, autoscaleSet, scalesetClient, logger)
	if err != nil {
		return r.updateStatusError(ctx, autoscaleSet, "ScaleSetNotFound",
			fmt.Sprintf("Failed to get scale set: %v", err), logger)
	}

	if err := r.Get(ctx, types.NamespacedName{
		Namespace: autoscaleSet.Namespace,
		Name:      autoscaleSet.Name,
	}, autoscaleSet); err != nil {
		return ctrl.Result{}, err
	}

	scaleSet, err = r.maybeUpdateScaleSetLabels(ctx, autoscaleSet, scalesetClient, scaleSet, logger)
	if err != nil {
		return r.updateStatusError(ctx, autoscaleSet, "ScaleSetUpdateFailed",
			fmt.Sprintf("Failed to update scale set labels: %v", err), logger)
	}

	r.setUpdateScaleSetStatus(ctx, autoscaleSet, scaleSet)
	if err := r.Status().Update(ctx, autoscaleSet); err != nil {
		if k8serrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	r.recordUpdateScaleSetMetrics(autoscaleSet)
	metrics.ReconciliationRate.WithLabelValues("autoscaleset", "success").Inc()
	if autoscaleSet.Status.Phase == constants.PhaseActive &&
		meta.IsStatusConditionTrue(autoscaleSet.Status.Conditions, "Ready") {
		return ctrl.Result{RequeueAfter: constants.RequeueActiveInterval}, nil
	}
	return ctrl.Result{}, nil
}

func (r *AutoscaleSetReconciler) getScaleSetForUpdate(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	scalesetClient *scaleset.Client,
	logger logr.Logger,
) (*scaleset.RunnerScaleSet, error) {
	return r.ClientFactory.GetCachedScaleSet(
		*autoscaleSet.Status.ScaleSetID,
		func() (*scaleset.RunnerScaleSet, error) {
			ss, getErr := scalesetClient.GetRunnerScaleSetByID(ctx, *autoscaleSet.Status.ScaleSetID)
			if getErr == nil && ss != nil {
				logger.V(1).Info("Retrieved scale set from GitHub", "scaleSetID", ss.ID, "labels", formatLabelsForLog(ss.Labels))
			}
			return ss, getErr
		},
	)
}

func (r *AutoscaleSetReconciler) maybeUpdateScaleSetLabels(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	scalesetClient *scaleset.Client,
	scaleSet *scaleset.RunnerScaleSet,
	logger logr.Logger,
) (*scaleset.RunnerScaleSet, error) {
	desiredLabels := r.buildLabels(autoscaleSet)
	if labelsEqual(scaleSet.Labels, desiredLabels) {
		return scaleSet, nil
	}
	logger.Info("Labels changed, updating scale set",
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
	var updated *scaleset.RunnerScaleSet
	err := client.RetryWithBackoff(ctx, client.DefaultRetryConfig, func() error {
		var retryErr error
		updated, retryErr = scalesetClient.UpdateRunnerScaleSet(ctx, *autoscaleSet.Status.ScaleSetID, updateScaleSet)
		if retryErr != nil {
			return retryErr
		}
		if updated != nil {
			logger.Info("Scale set updated by GitHub", "scaleSetID", updated.ID, "labels", formatLabelsForLog(updated.Labels))
		}
		return nil
	})
	if err != nil {
		logger.Error(err, "Failed to update scale set labels", "scaleSetID", *autoscaleSet.Status.ScaleSetID)
		return scaleSet, err
	}
	logger.Info("Successfully updated scale set labels", "scaleSetID", *autoscaleSet.Status.ScaleSetID, "labels", formatLabelsForLog(updated.Labels))
	r.Recorder.Eventf(autoscaleSet, nil, corev1.EventTypeNormal, "ScaleSetLabelsUpdated", "ScaleSetLabelsUpdated",
		"Updated labels for scale set %d", *autoscaleSet.Status.ScaleSetID,
	)
	return updated, nil
}

func (r *AutoscaleSetReconciler) setUpdateScaleSetStatus(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	scaleSet *scaleset.RunnerScaleSet,
) {
	if scaleSet.Labels != nil {
		autoscaleSet.Status.Labels = make([]scalesetv1alpha1.ScaleSetLabel, len(scaleSet.Labels))
		for i, label := range scaleSet.Labels {
			autoscaleSet.Status.Labels[i] = scalesetv1alpha1.ScaleSetLabel{Type: label.Type, Name: label.Name}
		}
	}
	if scaleSet.Statistics != nil {
		autoscaleSet.Status.CurrentRunners = int32(scaleSet.Statistics.TotalRegisteredRunners)
		autoscaleSet.Status.DesiredRunners = int32(scaleSet.Statistics.TotalAssignedJobs)
		setScaleSetStatistics(autoscaleSet.Namespace, autoscaleSet.Name, scaleSet.Statistics)
	} else {
		count, err := r.countRunnerPodsForAutoscaleSet(ctx, autoscaleSet.Namespace, fmt.Sprintf("%d", *autoscaleSet.Status.ScaleSetID))
		if err == nil {
			autoscaleSet.Status.CurrentRunners = int32(count)
		}
	}
	autoscaleSet.Status.Phase = constants.PhaseActive
	meta.SetStatusCondition(&autoscaleSet.Status.Conditions, metav1.Condition{
		Type: "Ready", Status: metav1.ConditionTrue, Reason: "ScaleSetActive",
		Message: "Scale set is active", ObservedGeneration: autoscaleSet.Generation,
	})
}

func (r *AutoscaleSetReconciler) recordUpdateScaleSetMetrics(autoscaleSet *scalesetv1alpha1.AutoscaleSet) {
	if autoscaleSet.Spec.MinRunners != nil {
		metrics.ScaleSetLimits.WithLabelValues(autoscaleSet.Namespace, autoscaleSet.Name, "min_runners").Set(float64(*autoscaleSet.Spec.MinRunners))
	}
	if autoscaleSet.Spec.MaxRunners != nil {
		metrics.ScaleSetLimits.WithLabelValues(autoscaleSet.Namespace, autoscaleSet.Name, "max_runners").Set(float64(*autoscaleSet.Spec.MaxRunners))
	}
	metrics.AutoscaleSetTotal.WithLabelValues(autoscaleSet.Namespace, autoscaleSet.Status.Phase).Set(1)
}

func (r *AutoscaleSetReconciler) handleDeletion(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	logger logr.Logger,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(autoscaleSet, constants.FinalizerName) {
		return ctrl.Result{}, nil
	}

	if autoscaleSet.Status.ScaleSetID != nil {
		if res, err := r.deleteScaleSetInGitHub(ctx, autoscaleSet, logger); err != nil {
			return res, err
		}
	}

	controllerutil.RemoveFinalizer(autoscaleSet, constants.FinalizerName)
	if err := r.Update(ctx, autoscaleSet); err != nil {
		return ctrl.Result{}, err
	}

	metrics.ScaleSetOperations.WithLabelValues(autoscaleSet.Namespace, autoscaleSet.Name, "delete", "success").Inc()
	metrics.AutoscaleSetTotal.WithLabelValues(autoscaleSet.Namespace, autoscaleSet.Status.Phase).Set(0)
	return ctrl.Result{}, nil
}

func (r *AutoscaleSetReconciler) deleteScaleSetInGitHub(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	logger logr.Logger,
) (ctrl.Result, error) {
	scalesetClient, err := r.ClientFactory.CreateClientForAutoscaleSet(ctx, autoscaleSet, client.SubsystemController)
	if err != nil {
		logger.Error(err, "Failed to create client for scale set deletion", "scaleSetID", *autoscaleSet.Status.ScaleSetID)
		return ctrl.Result{RequeueAfter: constants.DeletionRequeueAfter}, err
	}
	err = client.RetryWithBackoff(ctx, client.DefaultRetryConfig, func() error {
		return scalesetClient.DeleteRunnerScaleSet(ctx, *autoscaleSet.Status.ScaleSetID)
	})
	if err != nil {
		logger.Error(err, "Failed to delete scale set", "scaleSetID", *autoscaleSet.Status.ScaleSetID)
		return ctrl.Result{}, err
	}
	logger.Info("Deleted scale set", "scaleSetID", *autoscaleSet.Status.ScaleSetID)
	r.Recorder.Eventf(autoscaleSet, nil, corev1.EventTypeNormal, "ScaleSetDeleted", "ScaleSetDeleted",
		"Successfully deleted scale set with ID %d", *autoscaleSet.Status.ScaleSetID,
	)
	return ctrl.Result{}, nil
}

func (r *AutoscaleSetReconciler) updateStatusError(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	reason, message string,
	logger logr.Logger,
) (ctrl.Result, error) {
	key := types.NamespacedName{Namespace: autoscaleSet.Namespace, Name: autoscaleSet.Name}
	obj := &scalesetv1alpha1.AutoscaleSet{}
	err := RefetchAndUpdateStatus(ctx, r.Client, key, obj, func() {
		obj.Status.Phase = "Error"
		meta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionFalse,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: obj.Generation,
		})
	}, func(ctx context.Context, o k8sclient.Object) error { return r.Status().Update(ctx, o) })
	if err != nil {
		if errors.Is(err, ErrStatusConflict) {
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
		"Error",
	).Set(1)

	logger.Error(nil, message, "reason", reason)

	r.Recorder.Eventf(autoscaleSet, nil, corev1.EventTypeWarning, reason, reason, "%s", message)

	return ctrl.Result{RequeueAfter: constants.RequeueErrorInterval}, nil
}

func (r *AutoscaleSetReconciler) countRunnerPodsForAutoscaleSet(
	ctx context.Context,
	namespace, scaleSetID string,
) (int, error) {
	list := &corev1.PodList{}
	err := r.List(ctx, list,
		k8sclient.InNamespace(namespace),
		k8sclient.MatchingLabels{constants.LabelAutoscaleSet: scaleSetID},
	)
	if err != nil {
		return 0, err
	}
	return len(list.Items), nil
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

func setScaleSetStatistics(ns, name string, s *scaleset.RunnerScaleSetStatistic) {
	if s == nil {
		return
	}
	stats := []struct {
		label string
		value float64
	}{
		{"total_available_jobs", float64(s.TotalAvailableJobs)},
		{"total_acquired_jobs", float64(s.TotalAcquiredJobs)},
		{"total_assigned_jobs", float64(s.TotalAssignedJobs)},
		{"total_running_jobs", float64(s.TotalRunningJobs)},
		{"total_registered_runners", float64(s.TotalRegisteredRunners)},
		{"total_busy_runners", float64(s.TotalBusyRunners)},
		{"total_idle_runners", float64(s.TotalIdleRunners)},
	}
	for _, st := range stats {
		metrics.ScaleSetStatistics.WithLabelValues(ns, name, st.label).Set(st.value)
	}
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
