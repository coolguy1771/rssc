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
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/actions/scaleset"
	scalesetv1alpha1 "github.com/coolguy1771/rssc/api/v1alpha1"
	"github.com/coolguy1771/rssc/internal/client"
	"github.com/coolguy1771/rssc/internal/constants"
	rsscerrors "github.com/coolguy1771/rssc/internal/errors"
	"github.com/coolguy1771/rssc/internal/listener"
	"github.com/coolguy1771/rssc/internal/metrics"
	"github.com/coolguy1771/rssc/internal/scaler"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
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
	logger.Info("Reconciling RunnerSet", "namespace", req.Namespace, "name", req.Name)
	defer func() {
		metrics.ReconciliationDuration.WithLabelValues("runnerset", req.Namespace, req.Name).Observe(time.Since(startTime).Seconds())
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

	if !controllerutil.ContainsFinalizer(runnerSet, constants.RunnerSetFinalizerName) {
		controllerutil.AddFinalizer(runnerSet, constants.RunnerSetFinalizerName)
		if err := r.Update(ctx, runnerSet); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	autoscaleSet, err := r.getAutoscaleSet(ctx, runnerSet)
	if err != nil {
		return r.reconcileAutoscaleSetError(ctx, runnerSet, err, logger)
	}

	if !autoscaleSet.DeletionTimestamp.IsZero() {
		return r.updateStatusError(ctx, runnerSet, "AutoscaleSetDeleting",
			fmt.Sprintf("AutoscaleSet %s/%s is being deleted", autoscaleSet.Namespace, autoscaleSet.Name), logger)
	}

	if autoscaleSet.Status.ScaleSetID == nil {
		return ctrl.Result{RequeueAfter: constants.RequeueScaleSetIDWait}, nil
	}

	listenerKey := listener.ListenerKey{
		AutoscaleSet: types.NamespacedName{Namespace: autoscaleSet.Namespace, Name: autoscaleSet.Name},
		RunnerGroup:  runnerSet.Spec.RunnerGroup,
	}

	if r.ListenerManager.HasListener(listenerKey) {
		return r.reconcileExistingListener(ctx, runnerSet, autoscaleSet, listenerKey, logger)
	}
	return r.startListener(ctx, runnerSet, autoscaleSet, listenerKey, logger)
}

func (r *RunnerSetReconciler) reconcileAutoscaleSetError(
	ctx context.Context,
	runnerSet *scalesetv1alpha1.RunnerSet,
	err error,
	logger logr.Logger,
) (ctrl.Result, error) {
	if k8serrors.IsNotFound(err) {
		return r.updateStatusError(ctx, runnerSet, "AutoscaleSetNotFound",
			fmt.Sprintf("AutoscaleSet %s/%s not found", runnerSet.Spec.AutoscaleSetRef.Namespace, runnerSet.Spec.AutoscaleSetRef.Name), logger)
	}
	return r.updateStatusError(ctx, runnerSet, "AutoscaleSetError",
		fmt.Sprintf("Failed to get AutoscaleSet: %v", err), logger)
}

func (r *RunnerSetReconciler) reconcileExistingListener(
	ctx context.Context,
	runnerSet *scalesetv1alpha1.RunnerSet,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	listenerKey listener.ListenerKey,
	logger logr.Logger,
) (ctrl.Result, error) {
	currentPodSpecHash := r.computePodSpecHash(
		autoscaleSet.Spec.RunnerImage,
		runnerSet.Spec.RunnerTemplate,
		autoscaleSet.Spec.RunnerTemplate,
	)
	needsRollout := runnerSet.Annotations == nil ||
		runnerSet.Annotations[constants.AnnotationPodSpecHash] != currentPodSpecHash

	if !needsRollout {
		return r.updateStatus(ctx, runnerSet, logger)
	}

	oldHash := "none"
	if runnerSet.Annotations != nil && runnerSet.Annotations[constants.AnnotationPodSpecHash] != "" {
		oldHash = runnerSet.Annotations[constants.AnnotationPodSpecHash]
	}
	logger.Info("Pod spec or runner config changed, rolling out update",
		"autoscaleSet", autoscaleSet.Name, "oldHash", oldHash, "newHash", currentPodSpecHash)

	if err := r.rolloutRunnerPods(ctx, runnerSet, logger); err != nil {
		logger.Error(err, "Failed to rollout runner pods")
	}
	r.ListenerManager.StopListener(listenerKey)
	if runnerSet.Annotations == nil {
		runnerSet.Annotations = make(map[string]string)
	}
	runnerSet.Annotations[constants.AnnotationPodSpecHash] = currentPodSpecHash
	runnerSet.Annotations[constants.AnnotationLastAutoscaleSetGeneration] = fmt.Sprintf("%d", autoscaleSet.Generation)
	if err := r.Update(ctx, runnerSet); err != nil {
		logger.Error(err, "Failed to update RunnerSet annotations")
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
	scalesetClient, err := r.ClientFactory.CreateClientForAutoscaleSet(ctx, autoscaleSet, client.SubsystemListener)
	if err != nil {
		return r.updateStatusError(ctx, runnerSet, "ClientCreationFailed",
			fmt.Sprintf("Failed to create scaleset client: %v", err), logger)
	}

	sessionClient, res, err := r.createMessageSession(ctx, runnerSet, autoscaleSet, scalesetClient, listenerKey, logger)
	if err != nil || res != nil {
		if res != nil {
			return *res, err
		}
		return ctrl.Result{}, err
	}

	minRunners := constants.DefaultMinRunners
	if autoscaleSet.Spec.MinRunners != nil {
		minRunners = int(*autoscaleSet.Spec.MinRunners)
	}
	maxRunners := constants.DefaultMaxRunners
	if autoscaleSet.Spec.MaxRunners != nil {
		maxRunners = int(*autoscaleSet.Spec.MaxRunners)
	}

	podTemplate := r.mergeRunnerTemplates(autoscaleSet.Spec.RunnerTemplate, runnerSet.Spec.RunnerTemplate)
	kubernetesScaler := scaler.NewKubernetesScaler(
		r.Client, scalesetClient, *autoscaleSet.Status.ScaleSetID,
		runnerSet.Namespace, autoscaleSet.Spec.RunnerImage, runnerSet.Spec.RunnerGroup,
		minRunners, maxRunners, podTemplate, r.ClientFactory.GetRateLimiter(), logger,
	)
	kubernetesScaler.SetOwnerReference(autoscaleSet, autoscaleSet.APIVersion, autoscaleSet.Kind)
	debouncedScaler := scaler.NewDebouncedScaler(kubernetesScaler, constants.DebounceWindow, logger)

	if err := r.ListenerManager.StartListener(ctx, listenerKey, sessionClient,
		*autoscaleSet.Status.ScaleSetID, maxRunners, debouncedScaler); err != nil {
		return r.updateStatusError(ctx, runnerSet, "ListenerStartFailed",
			fmt.Sprintf("Failed to start listener: %v", err), logger)
	}

	r.Recorder.Event(runnerSet, corev1.EventTypeNormal, "ListenerStarted",
		fmt.Sprintf("Started listener for runner group %s", runnerSet.Spec.RunnerGroup),
	)

	if err := r.setRunnerSetListenerAnnotations(ctx, runnerSet, autoscaleSet); err != nil {
		return ctrl.Result{}, err
	}

	autoscaleSetName := runnerSet.Spec.AutoscaleSetRef.Name
	if autoscaleSetName == "" {
		autoscaleSetName = "unknown"
	}
	metrics.ListenerStatus.WithLabelValues(runnerSet.Namespace, autoscaleSetName, runnerSet.Spec.RunnerGroup).Set(1)
	return r.updateStatus(ctx, runnerSet, logger)
}

func (r *RunnerSetReconciler) createMessageSession(
	ctx context.Context,
	runnerSet *scalesetv1alpha1.RunnerSet,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
	scalesetClient *scaleset.Client,
	listenerKey listener.ListenerKey,
	logger logr.Logger,
) (*scaleset.MessageSessionClient, *ctrl.Result, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = fmt.Sprintf("controller-%s", runnerSet.UID)
	}
	sessionClient, err := scalesetClient.MessageSessionClient(ctx, *autoscaleSet.Status.ScaleSetID, hostname)
	if err == nil {
		metrics.SessionOperations.WithLabelValues(
			runnerSet.Namespace, autoscaleSet.Name, runnerSet.Spec.RunnerGroup, "create", "success",
		).Inc()
		return sessionClient, nil, nil
	}
	if rsscerrors.IsSessionConflict(err) {
		metrics.SessionOperations.WithLabelValues(
			runnerSet.Namespace, autoscaleSet.Name, runnerSet.Spec.RunnerGroup, "create", "conflict",
		).Inc()
		metrics.SessionConflicts.WithLabelValues(
			runnerSet.Namespace, autoscaleSet.Name, runnerSet.Spec.RunnerGroup, hostname,
		).Inc()
		if r.ListenerManager.HasListener(listenerKey) {
			logger.Info("Session already exists but listener is running, continuing", "error", err)
			res, _ := r.updateStatus(ctx, runnerSet, logger)
			return nil, &res, nil
		}
		logger.Info("Session conflict detected - previous session still active. Waiting for it to expire before retrying",
			"hostname", hostname, "error", err)
		return nil, &ctrl.Result{RequeueAfter: constants.SessionConflictRequeueAfter}, nil
	}
	metrics.SessionOperations.WithLabelValues(
		runnerSet.Namespace, autoscaleSet.Name, runnerSet.Spec.RunnerGroup, "create", "error",
	).Inc()
	res, updateErr := r.updateStatusError(ctx, runnerSet, "SessionCreationFailed",
		fmt.Sprintf("Failed to create message session: %v", err), logger)
	return nil, &res, updateErr
}

func (r *RunnerSetReconciler) setRunnerSetListenerAnnotations(
	ctx context.Context,
	runnerSet *scalesetv1alpha1.RunnerSet,
	autoscaleSet *scalesetv1alpha1.AutoscaleSet,
) error {
	if err := r.Get(ctx, types.NamespacedName{Namespace: runnerSet.Namespace, Name: runnerSet.Name}, runnerSet); err != nil {
		return err
	}
	if runnerSet.Annotations == nil {
		runnerSet.Annotations = make(map[string]string)
	}
	runnerSet.Annotations[constants.AnnotationPodSpecHash] = r.computePodSpecHash(
		autoscaleSet.Spec.RunnerImage, runnerSet.Spec.RunnerTemplate, autoscaleSet.Spec.RunnerTemplate,
	)
	runnerSet.Annotations[constants.AnnotationLastAutoscaleSetGeneration] = fmt.Sprintf("%d", autoscaleSet.Generation)
	return r.Update(ctx, runnerSet)
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
		if pod.Labels[constants.LabelRunnerState] == constants.RunnerStateIdle {
			idleCount++
		} else if pod.Labels[constants.LabelRunnerState] == constants.RunnerStateBusy {
			busyCount++
		}

		phase := string(pod.Status.Phase)
		if phase == "" {
			phase = "Unknown"
		}
		podStatusCounts[phase]++

		if !pod.CreationTimestamp.IsZero() {
			age := now.Sub(pod.CreationTimestamp.Time).Seconds()
			state := pod.Labels[constants.LabelRunnerState]
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
		return ctrl.Result{RequeueAfter: constants.RequeueActiveInterval}, nil
	}
	return ctrl.Result{RequeueAfter: constants.RequeueScaleSetIDWait}, nil
}

func (r *RunnerSetReconciler) getRunnerPods(
	ctx context.Context,
	runnerSet *scalesetv1alpha1.RunnerSet,
) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}

	if err := r.List(
		ctx,
		podList,
		k8sclient.InNamespace(runnerSet.Namespace),
		k8sclient.MatchingFields{
			"metadata.labels." + constants.LabelRunnerGroup: runnerSet.Spec.RunnerGroup,
		},
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
	key := types.NamespacedName{Namespace: runnerSet.Namespace, Name: runnerSet.Name}
	obj := &scalesetv1alpha1.RunnerSet{}
	err := RefetchAndUpdateStatus(ctx, r.Client, key, obj, func() {
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

	logger.Error(nil, message, "reason", reason)

	r.Recorder.Event(
		runnerSet,
		corev1.EventTypeWarning,
		reason,
		message,
	)

	return ctrl.Result{RequeueAfter: constants.RequeueErrorInterval}, nil
}

func (r *RunnerSetReconciler) handleDeletion(
	ctx context.Context,
	runnerSet *scalesetv1alpha1.RunnerSet,
	logger logr.Logger,
) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(runnerSet, constants.RunnerSetFinalizerName) {
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

	controllerutil.RemoveFinalizer(runnerSet, constants.RunnerSetFinalizerName)
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

	merged := &scalesetv1alpha1.RunnerPodTemplate{Spec: scalesetv1alpha1.RunnerPodSpec{}}
	mergeTemplateMetadata(merged, baseTemplate, overrideTemplate)
	merged.Spec.NodeSelector = mergeStringMaps(baseTemplate.Spec.NodeSelector, overrideTemplate.Spec.NodeSelector)
	merged.Spec.Resources.Limits = mergeStringMaps(baseTemplate.Spec.Resources.Limits, overrideTemplate.Spec.Resources.Limits)
	merged.Spec.Resources.Requests = mergeStringMaps(baseTemplate.Spec.Resources.Requests, overrideTemplate.Spec.Resources.Requests)
	merged.Spec.Env = mergeEnvVars(baseTemplate.Spec.Env, overrideTemplate.Spec.Env)
	merged.Spec.Tolerations = mergeTolerations(baseTemplate.Spec.Tolerations, overrideTemplate.Spec.Tolerations)
	return merged
}

func mergeTemplateMetadata(
	merged *scalesetv1alpha1.RunnerPodTemplate,
	base, override *scalesetv1alpha1.RunnerPodTemplate,
) {
	merged.Metadata.Labels = mergeStringMaps(base.Metadata.Labels, override.Metadata.Labels)
	merged.Metadata.Annotations = mergeStringMaps(base.Metadata.Annotations, override.Metadata.Annotations)
}

func mergeStringMaps(base, override map[string]string) map[string]string {
	if base == nil && override == nil {
		return nil
	}
	out := make(map[string]string)
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergeEnvVars(base, override []scalesetv1alpha1.EnvVar) []scalesetv1alpha1.EnvVar {
	envMap := make(map[string]string)
	for _, env := range base {
		envMap[env.Name] = env.Value
	}
	for _, env := range override {
		envMap[env.Name] = env.Value
	}
	if len(envMap) == 0 {
		return nil
	}
	out := make([]scalesetv1alpha1.EnvVar, 0, len(envMap))
	for name, value := range envMap {
		out = append(out, scalesetv1alpha1.EnvVar{Name: name, Value: value})
	}
	return out
}

func mergeTolerations(base, override []scalesetv1alpha1.Toleration) []scalesetv1alpha1.Toleration {
	tolerationMap := make(map[string]scalesetv1alpha1.Toleration)
	for _, tol := range base {
		key := fmt.Sprintf("%s:%s:%s:%s", tol.Key, tol.Operator, tol.Value, tol.Effect)
		tolerationMap[key] = tol
	}
	for _, tol := range override {
		key := fmt.Sprintf("%s:%s:%s:%s", tol.Key, tol.Operator, tol.Value, tol.Effect)
		tolerationMap[key] = tol
	}
	if len(tolerationMap) == 0 {
		return nil
	}
	out := make([]scalesetv1alpha1.Toleration, 0, len(tolerationMap))
	for _, tol := range tolerationMap {
		out = append(out, tol)
	}
	return out
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
		if pod.Labels[constants.LabelRunnerState] == constants.RunnerStateIdle {
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
