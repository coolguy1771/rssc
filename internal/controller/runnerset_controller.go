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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/actions/scaleset"
	scalesetv1alpha1 "github.com/coolguy1771/rssc/api/v1alpha1"
	"github.com/coolguy1771/rssc/internal/client"
	"github.com/coolguy1771/rssc/internal/listener"
	"github.com/coolguy1771/rssc/internal/metrics"
	"github.com/coolguy1771/rssc/internal/scaler"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
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
)

const (
	RunnerSetFinalizerName = "scaleset.actions.github.com/runnerset-finalizer"
)

// RunnerSetReconciler reconciles a RunnerSet object
type RunnerSetReconciler struct {
	k8sclient.Client
	Scheme          *runtime.Scheme
	ClientFactory   *client.ClientFactory
	ListenerManager *listener.Manager
	Recorder        record.EventRecorder
}

// +kubebuilder:rbac:groups=scaleset.actions.github.com,resources=runnersets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=scaleset.actions.github.com,resources=runnersets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=scaleset.actions.github.com,resources=runnersets/finalizers,verbs=update
// +kubebuilder:rbac:groups=scaleset.actions.github.com,resources=autoscalesets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *RunnerSetReconciler) Reconcile(
	ctx context.Context,
	req ctrl.Request,
) (ctrl.Result, error) {
	startTime := time.Now()
	logger := log.FromContext(ctx)
	defer func() {
		duration := time.Since(startTime).Seconds()
		metrics.ReconciliationDuration.WithLabelValues(
			"runnerset",
			req.Namespace,
			req.Name,
		).Observe(duration)
	}()

	runnerSet := &scalesetv1alpha1.RunnerSet{}
	if err := r.Get(ctx, req.NamespacedName, runnerSet); err != nil {
		if k8serrors.IsNotFound(err) {
			metrics.ReconciliationRate.WithLabelValues("runnerset", "not_found").Inc()
			return ctrl.Result{}, nil
		}
		metrics.ReconciliationRate.WithLabelValues("runnerset", "error").Inc()
		return ctrl.Result{}, err
	}

	if !runnerSet.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, runnerSet, logger)
	}

	if !controllerutil.ContainsFinalizer(runnerSet, RunnerSetFinalizerName) {
		controllerutil.AddFinalizer(runnerSet, RunnerSetFinalizerName)
		if err := r.Update(ctx, runnerSet); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	autoscaleSet, err := r.getAutoscaleSet(ctx, runnerSet)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			return r.updateStatusError(
				ctx,
				runnerSet,
				"AutoscaleSetNotFound",
				fmt.Sprintf("AutoscaleSet %s/%s not found", runnerSet.Spec.AutoscaleSetRef.Namespace, runnerSet.Spec.AutoscaleSetRef.Name),
				logger,
			)
		}
		return r.updateStatusError(
			ctx,
			runnerSet,
			"AutoscaleSetError",
			fmt.Sprintf("Failed to get AutoscaleSet: %v", err),
			logger,
		)
	}

	if !autoscaleSet.DeletionTimestamp.IsZero() {
		return r.updateStatusError(
			ctx,
			runnerSet,
			"AutoscaleSetDeleting",
			fmt.Sprintf("AutoscaleSet %s/%s is being deleted", autoscaleSet.Namespace, autoscaleSet.Name),
			logger,
		)
	}

	if autoscaleSet.Status.ScaleSetID == nil {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	listenerKey := listener.ListenerKey{
		AutoscaleSet: types.NamespacedName{
			Namespace: autoscaleSet.Namespace,
			Name:      autoscaleSet.Name,
		},
		RunnerGroup: runnerSet.Spec.RunnerGroup,
	}

	if r.ListenerManager.HasListener(listenerKey) {
		// Check if pod spec or runner config changed
		currentPodSpecHash := r.computePodSpecHash(
			autoscaleSet.Spec.RunnerImage,
			runnerSet.Spec.RunnerTemplate,
			autoscaleSet.Spec.RunnerTemplate,
		)

		needsRollout := false
		if runnerSet.Annotations != nil {
			lastHash := runnerSet.Annotations["scaleset.actions.github.com/pod-spec-hash"]
			if lastHash != currentPodSpecHash {
				needsRollout = true
			}
		} else {
			needsRollout = true
		}

		if needsRollout {
			oldHash := "none"
			if runnerSet.Annotations != nil {
				if h := runnerSet.Annotations["scaleset.actions.github.com/pod-spec-hash"]; h != "" {
					oldHash = h
				}
			}
			logger.Info(
				"Pod spec or runner config changed, rolling out update",
				"autoscaleSet", autoscaleSet.Name,
				"oldHash", oldHash,
				"newHash", currentPodSpecHash,
			)

			// Delete idle pods to force recreation with new spec
			if err := r.rolloutRunnerPods(ctx, runnerSet, logger); err != nil {
				logger.Error(err, "Failed to rollout runner pods")
			}

			// Restart listener with new configuration
			r.ListenerManager.StopListener(listenerKey)
			if runnerSet.Annotations == nil {
				runnerSet.Annotations = make(map[string]string)
			}
			runnerSet.Annotations["scaleset.actions.github.com/pod-spec-hash"] =
				currentPodSpecHash
			runnerSet.Annotations["scaleset.actions.github.com/last-autoscaleset-generation"] =
				fmt.Sprintf("%d", autoscaleSet.Generation)
			if err := r.Update(ctx, runnerSet); err != nil {
				logger.Error(err, "Failed to update RunnerSet annotations")
			}
			return r.startListener(ctx, runnerSet, autoscaleSet, listenerKey, logger)
		}

		return r.updateStatus(ctx, runnerSet, logger)
	}

	return r.startListener(ctx, runnerSet, autoscaleSet, listenerKey, logger)
}

func (r *RunnerSetReconciler) getAutoscaleSet(
	ctx context.Context,
	runnerSet *scalesetv1alpha1.RunnerSet,
) (*scalesetv1alpha1.AutoscaleSet, error) {
	autoscaleSet := &scalesetv1alpha1.AutoscaleSet{}
	key := types.NamespacedName{
		Namespace: runnerSet.Spec.AutoscaleSetRef.Namespace,
		Name:      runnerSet.Spec.AutoscaleSetRef.Name,
	}
	if key.Namespace == "" {
		key.Namespace = runnerSet.Namespace
	}

	if err := r.Get(ctx, key, autoscaleSet); err != nil {
		return nil, err
	}

	return autoscaleSet, nil
}

func (r *RunnerSetReconciler) startListener(
	ctx context.Context,
	runnerSet *scalesetv1alpha1.RunnerSet,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	listenerKey listener.ListenerKey,
	logger logr.Logger,
) (ctrl.Result, error) {
	scalesetClient, err := r.getScalesetClient(ctx, autoscaleSet)
	if err != nil {
		return r.updateStatusError(
			ctx,
			runnerSet,
			"ClientCreationFailed",
			fmt.Sprintf("Failed to create scaleset client: %v", err),
			logger,
		)
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = fmt.Sprintf("controller-%s", runnerSet.UID)
	}

	sessionClient, err := scalesetClient.MessageSessionClient(
		ctx,
		*autoscaleSet.Status.ScaleSetID,
		hostname,
	)
	if err != nil {
		errStr := err.Error()
		if (strings.Contains(errStr, "status=409") ||
			strings.Contains(errStr, "status=\"409\"")) &&
			strings.Contains(errStr, "already has an active session") {
			metrics.SessionOperations.WithLabelValues(
				runnerSet.Namespace,
				autoscaleSet.Name,
				runnerSet.Spec.RunnerGroup,
				"create",
				"conflict",
			).Inc()

			metrics.SessionConflicts.WithLabelValues(
				runnerSet.Namespace,
				autoscaleSet.Name,
				runnerSet.Spec.RunnerGroup,
				hostname,
			).Inc()

			if r.ListenerManager.HasListener(listenerKey) {
				logger.Info(
					"Session already exists but listener is running, continuing",
					"error", err,
				)
				return r.updateStatus(ctx, runnerSet, logger)
			}
			logger.Info(
				"Session conflict detected - previous session still active. Waiting for it to expire before retrying",
				"hostname", hostname,
				"error", err,
			)
			return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil
		}

		metrics.SessionOperations.WithLabelValues(
			runnerSet.Namespace,
			autoscaleSet.Name,
			runnerSet.Spec.RunnerGroup,
			"create",
			"error",
		).Inc()

		return r.updateStatusError(
			ctx,
			runnerSet,
			"SessionCreationFailed",
			fmt.Sprintf("Failed to create message session: %v", err),
			logger,
		)
	}

	metrics.SessionOperations.WithLabelValues(
		runnerSet.Namespace,
		autoscaleSet.Name,
		runnerSet.Spec.RunnerGroup,
		"create",
		"success",
	).Inc()

	maxRunners := 10
	if autoscaleSet.Spec.MaxRunners != nil {
		maxRunners = int(*autoscaleSet.Spec.MaxRunners)
	}

	minRunners := 0
	if autoscaleSet.Spec.MinRunners != nil {
		minRunners = int(*autoscaleSet.Spec.MinRunners)
	}

	podTemplate := r.mergeRunnerTemplates(
		autoscaleSet.Spec.RunnerTemplate,
		runnerSet.Spec.RunnerTemplate,
	)

	kubernetesScaler := scaler.NewKubernetesScaler(
		r.Client,
		scalesetClient,
		*autoscaleSet.Status.ScaleSetID,
		runnerSet.Namespace,
		autoscaleSet.Spec.RunnerImage,
		runnerSet.Spec.RunnerGroup,
		minRunners,
		maxRunners,
		podTemplate,
		logger,
	)

	kubernetesScaler.SetOwnerReference(
		autoscaleSet,
		autoscaleSet.APIVersion,
		autoscaleSet.Kind,
	)

	if err := r.ListenerManager.StartListener(
		ctx,
		listenerKey,
		sessionClient,
		*autoscaleSet.Status.ScaleSetID,
		maxRunners,
		kubernetesScaler,
	); err != nil {
		return r.updateStatusError(
			ctx,
			runnerSet,
			"ListenerStartFailed",
			fmt.Sprintf("Failed to start listener: %v", err),
			logger,
		)
	}

	r.Recorder.Event(
		runnerSet,
		corev1.EventTypeNormal,
		"ListenerStarted",
		fmt.Sprintf("Started listener for runner group %s", runnerSet.Spec.RunnerGroup),
	)

	// Update annotation to track AutoscaleSet generation
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: runnerSet.Namespace,
		Name:      runnerSet.Name,
	}, runnerSet); err != nil {
		return ctrl.Result{}, err
	}
	if runnerSet.Annotations == nil {
		runnerSet.Annotations = make(map[string]string)
	}
	// Store pod spec hash for change detection
	podSpecHash := r.computePodSpecHash(
		autoscaleSet.Spec.RunnerImage,
		runnerSet.Spec.RunnerTemplate,
		autoscaleSet.Spec.RunnerTemplate,
	)
	runnerSet.Annotations["scaleset.actions.github.com/pod-spec-hash"] =
		podSpecHash
	runnerSet.Annotations["scaleset.actions.github.com/last-autoscaleset-generation"] =
		fmt.Sprintf("%d", autoscaleSet.Generation)
	if err := r.Update(ctx, runnerSet); err != nil {
		logger.Error(err, "Failed to update RunnerSet annotations")
	}

	autoscaleSetName := runnerSet.Spec.AutoscaleSetRef.Name
	if autoscaleSetName == "" {
		autoscaleSetName = "unknown"
	}

	metrics.ListenerStatus.WithLabelValues(
		runnerSet.Namespace,
		autoscaleSetName,
		runnerSet.Spec.RunnerGroup,
	).Set(1)

	return r.updateStatus(ctx, runnerSet, logger)
}

func (r *RunnerSetReconciler) getScalesetClient(
	ctx context.Context,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
) (*scaleset.Client, error) {
	config := client.ClientConfig{
		GitHubConfigURL: autoscaleSet.Spec.GitHubConfigURL,
		SystemInfo: scaleset.SystemInfo{
			System:     "actions-runner-controller",
			Version:    "v1alpha1",
			CommitSHA:  "unknown",
			ScaleSetID: *autoscaleSet.Status.ScaleSetID,
			Subsystem:  "listener",
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

	return r.ClientFactory.CreateClient(ctx, config)
}

func (r *RunnerSetReconciler) updateStatus(
	ctx context.Context,
	runnerSet *scalesetv1alpha1.RunnerSet,
	logger logr.Logger,
) (ctrl.Result, error) {
	pods, err := r.getRunnerPods(ctx, runnerSet)
	if err != nil {
		return ctrl.Result{}, err
	}

	var idleCount, busyCount int32
	podStatusCounts := make(map[string]int32)
	now := time.Now()

	autoscaleSetName := runnerSet.Spec.AutoscaleSetRef.Name
	if autoscaleSetName == "" {
		autoscaleSetName = "unknown"
	}

	for _, pod := range pods {
		if pod.Labels[scaler.LabelRunnerState] == scaler.RunnerStateIdle {
			idleCount++
		} else if pod.Labels[scaler.LabelRunnerState] == scaler.RunnerStateBusy {
			busyCount++
		}

		phase := string(pod.Status.Phase)
		if phase == "" {
			phase = "Unknown"
		}
		podStatusCounts[phase]++

		if !pod.CreationTimestamp.IsZero() {
			age := now.Sub(pod.CreationTimestamp.Time).Seconds()
			state := pod.Labels[scaler.LabelRunnerState]
			if state == "" {
				state = "unknown"
			}
			metrics.RunnerPodAge.WithLabelValues(
				runnerSet.Namespace,
				autoscaleSetName,
				runnerSet.Spec.RunnerGroup,
				state,
			).Observe(age)
		}
	}

	if err := r.Get(ctx, types.NamespacedName{
		Namespace: runnerSet.Namespace,
		Name:      runnerSet.Name,
	}, runnerSet); err != nil {
		return ctrl.Result{}, err
	}

	runnerSet.Status.RunnerCount = int32(len(pods))
	runnerSet.Status.IdleRunners = idleCount
	runnerSet.Status.BusyRunners = busyCount
	meta.SetStatusCondition(&runnerSet.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionTrue,
		Reason:             "ListenerActive",
		Message:            "Listener is active",
		ObservedGeneration: runnerSet.Generation,
	})

	if err := r.Status().Update(ctx, runnerSet); err != nil {
		if k8serrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	metrics.RunnerSetTotal.WithLabelValues(
		runnerSet.Namespace,
		"active",
	).Set(1)

	metrics.RunnerPodsTotal.WithLabelValues(
		runnerSet.Namespace,
		autoscaleSetName,
		runnerSet.Spec.RunnerGroup,
		"idle",
	).Set(float64(idleCount))

	metrics.RunnerPodsTotal.WithLabelValues(
		runnerSet.Namespace,
		autoscaleSetName,
		runnerSet.Spec.RunnerGroup,
		"busy",
	).Set(float64(busyCount))

	metrics.RunnerPodsIdle.WithLabelValues(
		runnerSet.Namespace,
		autoscaleSetName,
		runnerSet.Spec.RunnerGroup,
	).Set(float64(idleCount))

	metrics.RunnerPodsBusy.WithLabelValues(
		runnerSet.Namespace,
		autoscaleSetName,
		runnerSet.Spec.RunnerGroup,
	).Set(float64(busyCount))

	metrics.ListenerStatus.WithLabelValues(
		runnerSet.Namespace,
		autoscaleSetName,
		runnerSet.Spec.RunnerGroup,
	).Set(1)

	for phase, count := range podStatusCounts {
		metrics.RunnerPodStatus.WithLabelValues(
			runnerSet.Namespace,
			autoscaleSetName,
			runnerSet.Spec.RunnerGroup,
			phase,
		).Set(float64(count))
	}

	metrics.ReconciliationRate.WithLabelValues("runnerset", "success").Inc()
	if runnerSet.Status.RunnerCount > 0 ||
		meta.IsStatusConditionTrue(
			runnerSet.Status.Conditions,
			"Ready",
		) {
		return ctrl.Result{RequeueAfter: 2 * time.Minute}, nil
	}
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *RunnerSetReconciler) getRunnerPods(
	ctx context.Context,
	runnerSet *scalesetv1alpha1.RunnerSet,
) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	selector := labels.SelectorFromSet(labels.Set{
		scaler.LabelRunnerGroup: runnerSet.Spec.RunnerGroup,
	})

	if err := r.List(
		ctx,
		podList,
		k8sclient.InNamespace(runnerSet.Namespace),
		k8sclient.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		return nil, err
	}

	return podList.Items, nil
}

func (r *RunnerSetReconciler) updateStatusError(
	ctx context.Context,
	runnerSet *scalesetv1alpha1.RunnerSet,
	reason, message string,
	logger logr.Logger,
) (ctrl.Result, error) {
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: runnerSet.Namespace,
		Name:      runnerSet.Name,
	}, runnerSet); err != nil {
		return ctrl.Result{}, err
	}

	meta.SetStatusCondition(&runnerSet.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: runnerSet.Generation,
	})

	if err := r.Status().Update(ctx, runnerSet); err != nil {
		if k8serrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	}

	logger.Error(nil, message, "reason", reason)

	r.Recorder.Event(
		runnerSet,
		corev1.EventTypeWarning,
		reason,
		message,
	)

	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

func (r *RunnerSetReconciler) handleDeletion(
	ctx context.Context,
	runnerSet *scalesetv1alpha1.RunnerSet,
	logger logr.Logger,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(runnerSet, RunnerSetFinalizerName) {
		return ctrl.Result{}, nil
	}

	autoscaleSet, err := r.getAutoscaleSet(ctx, runnerSet)
	if err == nil && autoscaleSet.Status.ScaleSetID != nil {
		listenerKey := listener.ListenerKey{
			AutoscaleSet: types.NamespacedName{
				Namespace: autoscaleSet.Namespace,
				Name:      autoscaleSet.Name,
			},
			RunnerGroup: runnerSet.Spec.RunnerGroup,
		}

		if r.ListenerManager.HasListener(listenerKey) {
			r.ListenerManager.StopListener(listenerKey)
			logger.Info(
				"Stopped listener for runner set",
				"runnerGroup", runnerSet.Spec.RunnerGroup,
			)

			r.Recorder.Event(
				runnerSet,
				corev1.EventTypeNormal,
				"ListenerStopped",
				fmt.Sprintf("Stopped listener for runner group %s", runnerSet.Spec.RunnerGroup),
			)
		}
	}

	controllerutil.RemoveFinalizer(runnerSet, RunnerSetFinalizerName)
	if err := r.Update(ctx, runnerSet); err != nil {
		return ctrl.Result{}, err
	}

	autoscaleSetName := runnerSet.Spec.AutoscaleSetRef.Name
	if autoscaleSetName == "" {
		autoscaleSetName = "unknown"
	}

	metrics.RunnerSetTotal.WithLabelValues(
		runnerSet.Namespace,
		"deleted",
	).Set(0)

	metrics.ListenerStatus.WithLabelValues(
		runnerSet.Namespace,
		autoscaleSetName,
		runnerSet.Spec.RunnerGroup,
	).Set(0)

	return ctrl.Result{}, nil
}

func (r *RunnerSetReconciler) mapAutoscaleSetToRunnerSets(
	ctx context.Context,
	obj k8sclient.Object,
) []ctrl.Request {
	autoscaleSet, ok := obj.(*scalesetv1alpha1.AutoscaleSet)
	if !ok {
		return nil
	}

	runnerSetList := &scalesetv1alpha1.RunnerSetList{}
	if err := r.List(ctx, runnerSetList); err != nil {
		return nil
	}

	var requests []ctrl.Request
	for _, runnerSet := range runnerSetList.Items {
		refNamespace := runnerSet.Spec.AutoscaleSetRef.Namespace
		if refNamespace == "" {
			refNamespace = runnerSet.Namespace
		}
		if runnerSet.Spec.AutoscaleSetRef.Name == autoscaleSet.Name &&
			refNamespace == autoscaleSet.Namespace {
			requests = append(requests, ctrl.Request{
				NamespacedName: types.NamespacedName{
					Namespace: runnerSet.Namespace,
					Name:      runnerSet.Name,
				},
			})
		}
	}

	return requests
}

func (r *RunnerSetReconciler) mergeRunnerTemplates(
	baseTemplate *scalesetv1alpha1.RunnerPodTemplate,
	overrideTemplate *scalesetv1alpha1.RunnerPodTemplate,
) *scalesetv1alpha1.RunnerPodTemplate {
	if baseTemplate == nil && overrideTemplate == nil {
		return nil
	}
	if baseTemplate == nil {
		return r.copyTemplateWithCleanMetadata(overrideTemplate)
	}
	if overrideTemplate == nil {
		return r.copyTemplateWithCleanMetadata(baseTemplate)
	}

	merged := &scalesetv1alpha1.RunnerPodTemplate{
		Spec: scalesetv1alpha1.RunnerPodSpec{},
	}

	hasLabels := (baseTemplate.Metadata.Labels != nil && len(baseTemplate.Metadata.Labels) > 0) ||
		(overrideTemplate.Metadata.Labels != nil && len(overrideTemplate.Metadata.Labels) > 0)
	hasAnnotations := (baseTemplate.Metadata.Annotations != nil && len(baseTemplate.Metadata.Annotations) > 0) ||
		(overrideTemplate.Metadata.Annotations != nil && len(overrideTemplate.Metadata.Annotations) > 0)

	if hasLabels {
		merged.Metadata.Labels = make(map[string]string)
		if baseTemplate.Metadata.Labels != nil {
			for k, v := range baseTemplate.Metadata.Labels {
				merged.Metadata.Labels[k] = v
			}
		}
		if overrideTemplate.Metadata.Labels != nil {
			for k, v := range overrideTemplate.Metadata.Labels {
				merged.Metadata.Labels[k] = v
			}
		}
	}

	if hasAnnotations {
		merged.Metadata.Annotations = make(map[string]string)
		if baseTemplate.Metadata.Annotations != nil {
			for k, v := range baseTemplate.Metadata.Annotations {
				merged.Metadata.Annotations[k] = v
			}
		}
		if overrideTemplate.Metadata.Annotations != nil {
			for k, v := range overrideTemplate.Metadata.Annotations {
				merged.Metadata.Annotations[k] = v
			}
		}
	}

	if baseTemplate.Spec.NodeSelector != nil || overrideTemplate.Spec.NodeSelector != nil {
		merged.Spec.NodeSelector = make(map[string]string)
		if baseTemplate.Spec.NodeSelector != nil {
			for k, v := range baseTemplate.Spec.NodeSelector {
				merged.Spec.NodeSelector[k] = v
			}
		}
		if overrideTemplate.Spec.NodeSelector != nil {
			for k, v := range overrideTemplate.Spec.NodeSelector {
				merged.Spec.NodeSelector[k] = v
			}
		}
		if len(merged.Spec.NodeSelector) == 0 {
			merged.Spec.NodeSelector = nil
		}
	}

	if baseTemplate.Spec.Resources.Limits != nil || overrideTemplate.Spec.Resources.Limits != nil {
		merged.Spec.Resources.Limits = make(map[string]string)
		if baseTemplate.Spec.Resources.Limits != nil {
			for k, v := range baseTemplate.Spec.Resources.Limits {
				merged.Spec.Resources.Limits[k] = v
			}
		}
		if overrideTemplate.Spec.Resources.Limits != nil {
			for k, v := range overrideTemplate.Spec.Resources.Limits {
				merged.Spec.Resources.Limits[k] = v
			}
		}
		if len(merged.Spec.Resources.Limits) == 0 {
			merged.Spec.Resources.Limits = nil
		}
	}

	if baseTemplate.Spec.Resources.Requests != nil || overrideTemplate.Spec.Resources.Requests != nil {
		merged.Spec.Resources.Requests = make(map[string]string)
		if baseTemplate.Spec.Resources.Requests != nil {
			for k, v := range baseTemplate.Spec.Resources.Requests {
				merged.Spec.Resources.Requests[k] = v
			}
		}
		if overrideTemplate.Spec.Resources.Requests != nil {
			for k, v := range overrideTemplate.Spec.Resources.Requests {
				merged.Spec.Resources.Requests[k] = v
			}
		}
		if len(merged.Spec.Resources.Requests) == 0 {
			merged.Spec.Resources.Requests = nil
		}
	}

	envMap := make(map[string]string)
	if baseTemplate.Spec.Env != nil {
		for _, env := range baseTemplate.Spec.Env {
			envMap[env.Name] = env.Value
		}
	}
	if overrideTemplate.Spec.Env != nil {
		for _, env := range overrideTemplate.Spec.Env {
			envMap[env.Name] = env.Value
		}
	}
	if len(envMap) > 0 {
		merged.Spec.Env = make([]scalesetv1alpha1.EnvVar, 0, len(envMap))
		for name, value := range envMap {
			merged.Spec.Env = append(merged.Spec.Env, scalesetv1alpha1.EnvVar{
				Name:  name,
				Value: value,
			})
		}
	}

	tolerationMap := make(map[string]scalesetv1alpha1.Toleration)
	if baseTemplate.Spec.Tolerations != nil {
		for _, tol := range baseTemplate.Spec.Tolerations {
			key := fmt.Sprintf("%s:%s:%s:%s", tol.Key, tol.Operator, tol.Value, tol.Effect)
			tolerationMap[key] = tol
		}
	}
	if overrideTemplate.Spec.Tolerations != nil {
		for _, tol := range overrideTemplate.Spec.Tolerations {
			key := fmt.Sprintf("%s:%s:%s:%s", tol.Key, tol.Operator, tol.Value, tol.Effect)
			tolerationMap[key] = tol
		}
	}
	if len(tolerationMap) > 0 {
		merged.Spec.Tolerations = make([]scalesetv1alpha1.Toleration, 0, len(tolerationMap))
		for _, tol := range tolerationMap {
			merged.Spec.Tolerations = append(merged.Spec.Tolerations, tol)
		}
	}

	return merged
}

func (r *RunnerSetReconciler) copyTemplateWithCleanMetadata(
	template *scalesetv1alpha1.RunnerPodTemplate,
) *scalesetv1alpha1.RunnerPodTemplate {
	if template == nil {
		return nil
	}
	copied := &scalesetv1alpha1.RunnerPodTemplate{}
	if template.Metadata.Labels != nil || template.Metadata.Annotations != nil {
		if template.Metadata.Labels != nil {
			copied.Metadata.Labels = make(map[string]string)
			for k, v := range template.Metadata.Labels {
				copied.Metadata.Labels[k] = v
			}
		}
		if template.Metadata.Annotations != nil {
			copied.Metadata.Annotations = make(map[string]string)
			for k, v := range template.Metadata.Annotations {
				copied.Metadata.Annotations[k] = v
			}
		}
	}
	copied.Spec = template.Spec
	return copied
}

func (r *RunnerSetReconciler) computePodSpecHash(
	runnerImage string,
	runnerSetTemplate *scalesetv1alpha1.RunnerPodTemplate,
	autoscaleSetTemplate *scalesetv1alpha1.RunnerPodTemplate,
) string {
	type podSpecConfig struct {
		Image                string
		RunnerSetTemplate    *scalesetv1alpha1.RunnerPodTemplate
		AutoscaleSetTemplate *scalesetv1alpha1.RunnerPodTemplate
	}

	config := podSpecConfig{
		Image:                runnerImage,
		RunnerSetTemplate:    runnerSetTemplate,
		AutoscaleSetTemplate: autoscaleSetTemplate,
	}

	configBytes, err := json.Marshal(config)
	if err != nil {
		return fmt.Sprintf("error-%d", time.Now().Unix())
	}

	hash := sha256.Sum256(configBytes)
	return hex.EncodeToString(hash[:])[:16]
}

func (r *RunnerSetReconciler) rolloutRunnerPods(
	ctx context.Context,
	runnerSet *scalesetv1alpha1.RunnerSet,
	logger logr.Logger,
) error {
	pods, err := r.getRunnerPods(ctx, runnerSet)
	if err != nil {
		return fmt.Errorf("failed to get runner pods: %w", err)
	}

	deletedCount := 0
	for _, pod := range pods {
		// Only delete idle pods to avoid disrupting running jobs
		if pod.Labels[scaler.LabelRunnerState] == scaler.RunnerStateIdle {
			logger.Info(
				"Deleting idle pod for rollout",
				"podName", pod.Name,
			)
			if err := r.Delete(ctx, &pod); err != nil && !k8serrors.IsNotFound(err) {
				logger.Error(
					err,
					"Failed to delete pod during rollout",
					"podName", pod.Name,
				)
			} else {
				deletedCount++
			}
		}
	}

	if deletedCount > 0 {
		logger.Info(
			"Rolled out runner pods",
			"deletedCount", deletedCount,
			"totalPods", len(pods),
		)
		r.Recorder.Event(
			runnerSet,
			corev1.EventTypeNormal,
			"RunnerPodsRolledOut",
			fmt.Sprintf("Rolled out %d idle runner pods", deletedCount),
		)
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RunnerSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&scalesetv1alpha1.RunnerSet{}).
		Named("runnerset").
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 10,
		}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Watches(
			&scalesetv1alpha1.AutoscaleSet{},
			handler.EnqueueRequestsFromMapFunc(r.mapAutoscaleSetToRunnerSets),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}
