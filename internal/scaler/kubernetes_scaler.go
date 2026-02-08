package scaler

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/actions/scaleset"
	scalesetv1alpha1 "github.com/coolguy1771/rssc/api/v1alpha1"
	"github.com/coolguy1771/rssc/internal/client"
	"github.com/coolguy1771/rssc/internal/constants"
	"github.com/coolguy1771/rssc/internal/metrics"
	"github.com/go-logr/logr"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
)

type KubernetesScaler struct {
	k8sClient      k8sclient.Client
	scalesetClient *scaleset.Client
	scaleSetID     int
	namespace      string
	runnerImage    string
	runnerGroup    string
	minRunners     int
	maxRunners     int
	template       *scalesetv1alpha1.RunnerPodTemplate
	ownerRef       *metav1.OwnerReference
	rateLimiter    *client.RateLimiter
	logger         logr.Logger
}

func NewKubernetesScaler(
	k8sClient k8sclient.Client,
	scalesetClient *scaleset.Client,
	scaleSetID int,
	namespace string,
	runnerImage string,
	runnerGroup string,
	minRunners int,
	maxRunners int,
	template *scalesetv1alpha1.RunnerPodTemplate,
	rateLimiter *client.RateLimiter,
	logger logr.Logger,
) *KubernetesScaler {
	return &KubernetesScaler{
		k8sClient:      k8sClient,
		scalesetClient: scalesetClient,
		scaleSetID:     scaleSetID,
		namespace:      namespace,
		runnerImage:    runnerImage,
		runnerGroup:    runnerGroup,
		minRunners:     minRunners,
		maxRunners:     maxRunners,
		template:       template,
		rateLimiter:    rateLimiter,
		logger:         logger,
	}
}

func (k *KubernetesScaler) waitForRateLimit(ctx context.Context) error {
	if k.rateLimiter != nil {
		return k.rateLimiter.Wait(ctx)
	}
	return nil
}

func (k *KubernetesScaler) SetOwnerReference(owner metav1.Object, apiVersion, kind string) {
	k.ownerRef = &metav1.OwnerReference{
		APIVersion:         apiVersion,
		Kind:               kind,
		Name:               owner.GetName(),
		UID:                owner.GetUID(),
		Controller:         func() *bool { b := false; return &b }(),
		BlockOwnerDeletion: func() *bool { b := true; return &b }(),
	}
}

func (k *KubernetesScaler) HandleDesiredRunnerCount(
	ctx context.Context,
	count int,
) (int, error) {
	currentPods, err := k.getRunnerPods(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get runner pods: %w", err)
	}

	currentCount := len(currentPods)
	targetCount := k.minRunners + count
	if targetCount > k.maxRunners {
		targetCount = k.maxRunners
	}

	k.logger.Info(
		"Handling desired runner count",
		"current", currentCount,
		"desired", targetCount,
		"minRunners", k.minRunners,
		"maxRunners", k.maxRunners,
		"queuedJobs", count,
	)

	metrics.QueuedJobs.WithLabelValues(
		k.namespace,
		fmt.Sprintf("%d", k.scaleSetID),
		k.runnerGroup,
	).Set(float64(count))

	if targetCount > currentCount {
		return k.handleScaleUp(ctx, currentCount, targetCount)
	}
	if targetCount < currentCount {
		return k.handleScaleDown(ctx, currentPods, currentCount, targetCount)
	}
	return currentCount, nil
}

func (k *KubernetesScaler) handleScaleUp(
	ctx context.Context,
	currentCount, targetCount int,
) (int, error) {
	scaleUp := targetCount - currentCount
	if targetCount == k.maxRunners && currentCount < k.maxRunners {
		metrics.ScalingConstraints.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			"max_runners",
		).Inc()
	}
	created := k.batchCreateRunnerPods(ctx, scaleUp)
	if created < scaleUp {
		metrics.ScalingConstraints.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			"partial_scale_up",
		).Inc()
	}
	if created > 0 {
		metrics.RunnerScalingOperations.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			"scale_up",
			"success",
		).Inc()
		metrics.RunnerScalingAmount.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			"scale_up",
		).Observe(float64(created))
	}
	if created < scaleUp {
		metrics.RunnerScalingOperations.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			"scale_up",
			"error",
		).Inc()
		return currentCount + created, fmt.Errorf(
			"failed to create all runner pods: created %d of %d",
			created,
			scaleUp,
		)
	}
	return targetCount, nil
}

func (k *KubernetesScaler) handleScaleDown(
	ctx context.Context,
	currentPods []corev1.Pod,
	currentCount, targetCount int,
) (int, error) {
	scaleDown := currentCount - targetCount
	idlePods := k.filterIdlePods(currentPods)
	toDelete := scaleDown
	if len(idlePods) < toDelete {
		toDelete = len(idlePods)
	}

	deleted := k.batchDeleteRunnerPods(ctx, idlePods[:toDelete])
	if deleted > 0 {
		metrics.RunnerScalingOperations.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			"scale_down",
			"success",
		).Inc()
		metrics.RunnerScalingAmount.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			"scale_down",
		).Observe(float64(deleted))
	}
	if deleted < toDelete {
		metrics.RunnerScalingOperations.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			"scale_down",
			"error",
		).Inc()
		return currentCount - deleted, fmt.Errorf(
			"failed to delete all runner pods: deleted %d of %d",
			deleted,
			toDelete,
		)
	}
	return currentCount - toDelete, nil
}

func (k *KubernetesScaler) HandleJobStarted(
	ctx context.Context,
	jobInfo *scaleset.JobStarted,
) error {
	k.logger.Info(
		"Job started",
		"runnerName", jobInfo.RunnerName,
		"jobId", jobInfo.JobID,
	)

	pod, err := k.findRunnerPod(ctx, jobInfo.RunnerName)
	if err != nil {
		return fmt.Errorf("failed to find runner pod: %w", err)
	}

	if pod == nil {
		k.logger.Info(
			"Runner pod not found for job started",
			"runnerName", jobInfo.RunnerName,
		)
		return nil
	}

	oldState := pod.Labels[constants.LabelRunnerState]
	pod.Labels[constants.LabelRunnerState] = constants.RunnerStateBusy
	if err := k.k8sClient.Update(ctx, pod); err != nil {
		return fmt.Errorf("failed to update runner pod: %w", err)
	}

	if oldState != constants.RunnerStateBusy {
		metrics.RunnerStateTransitions.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			oldState,
			constants.RunnerStateBusy,
		).Inc()
	}

	metrics.JobLifecycle.WithLabelValues(
		k.namespace,
		fmt.Sprintf("%d", k.scaleSetID),
		k.runnerGroup,
		"started",
		"success",
	).Inc()

	if jobInfo.RepositoryName != "" || jobInfo.JobWorkflowRef != "" {
		k.logger.V(1).Info(
			"Job started details",
			"runnerName", jobInfo.RunnerName,
			"repository", jobInfo.RepositoryName,
			"workflow", jobInfo.JobWorkflowRef,
		)
	}

	if !jobInfo.QueueTime.IsZero() && !jobInfo.RunnerAssignTime.IsZero() {
		queueTime := jobInfo.RunnerAssignTime.Sub(jobInfo.QueueTime).Seconds()
		metrics.JobQueueTime.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
		).Observe(queueTime)
	}

	return nil
}

func (k *KubernetesScaler) HandleJobCompleted(
	ctx context.Context,
	jobInfo *scaleset.JobCompleted,
) error {
	k.logger.Info(
		"Job completed",
		"runnerName", jobInfo.RunnerName,
		"jobId", jobInfo.JobID,
		"result", jobInfo.Result,
	)

	pod, err := k.findRunnerPod(ctx, jobInfo.RunnerName)
	if err != nil {
		return fmt.Errorf("failed to find runner pod: %w", err)
	}

	if pod == nil {
		k.logger.Info(
			"Runner pod not found for job completed",
			"runnerName", jobInfo.RunnerName,
		)
		return nil
	}

	metrics.JobLifecycle.WithLabelValues(
		k.namespace,
		fmt.Sprintf("%d", k.scaleSetID),
		k.runnerGroup,
		"completed",
		jobInfo.Result,
	).Inc()

	if jobInfo.RepositoryName != "" || jobInfo.JobWorkflowRef != "" {
		k.logger.V(1).Info(
			"Job completed details",
			"runnerName", jobInfo.RunnerName,
			"repository", jobInfo.RepositoryName,
			"workflow", jobInfo.JobWorkflowRef,
			"result", jobInfo.Result,
		)
	}

	if !jobInfo.RunnerAssignTime.IsZero() && !jobInfo.FinishTime.IsZero() {
		executionTime := jobInfo.FinishTime.Sub(jobInfo.RunnerAssignTime).Seconds()
		metrics.JobExecutionTime.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			jobInfo.Result,
		).Observe(executionTime)
	}

	if err := k.deleteRunnerPod(ctx, pod); err != nil {
		k.logger.Error(
			err,
			"Failed to delete runner pod after job completion",
			"podName", pod.Name,
			"runnerName", jobInfo.RunnerName,
		)
		return fmt.Errorf("failed to delete runner pod: %w", err)
	}

	metrics.RunnerPodLifecycle.WithLabelValues(
		k.namespace,
		fmt.Sprintf("%d", k.scaleSetID),
		k.runnerGroup,
		"deleted",
	).Inc()

	k.logger.Info(
		"Cleaned up runner pod after job completion",
		"podName", pod.Name,
		"runnerName", jobInfo.RunnerName,
		"jobId", jobInfo.JobID,
		"result", jobInfo.Result,
	)

	return nil
}

func (k *KubernetesScaler) getRunnerPods(
	ctx context.Context,
) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}

	if err := k.k8sClient.List(
		ctx,
		podList,
		k8sclient.InNamespace(k.namespace),
		k8sclient.MatchingFields{
			"metadata.labels." + constants.LabelAutoscaleSet: fmt.Sprintf("%d", k.scaleSetID),
		},
	); err != nil {
		return nil, err
	}

	activePods, podsToCleanup := k.classifyRunnerPods(podList.Items)
	if len(podsToCleanup) > 0 {
		k.cleanupTerminatedPodsInBackground(podsToCleanup)
	}

	return activePods, nil
}

func (k *KubernetesScaler) classifyRunnerPods(
	items []corev1.Pod,
) (activePods []corev1.Pod, podsToCleanup []corev1.Pod) {
	for _, pod := range items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		phase := pod.Status.Phase
		if phase == corev1.PodSucceeded || phase == corev1.PodFailed {
			podsToCleanup = append(podsToCleanup, pod)
			continue
		}
		if phase == corev1.PodPending {
			if time.Since(pod.CreationTimestamp.Time) > constants.PendingPodCleanupThreshold {
				k.logger.Info(
					"Cleaning up pod stuck in Pending state",
					"podName", pod.Name,
					"age", time.Since(pod.CreationTimestamp.Time),
				)
				podsToCleanup = append(podsToCleanup, pod)
				continue
			}
		}
		activePods = append(activePods, pod)
	}
	return activePods, podsToCleanup
}

func (k *KubernetesScaler) cleanupTerminatedPodsInBackground(pods []corev1.Pod) {
	podsCopy := make([]corev1.Pod, len(pods))
	copy(podsCopy, pods)
	go func() {
		cleanupCtx, cancel := context.WithTimeout(
			context.Background(),
			constants.CleanupTimeout,
		)
		defer cancel()

		for _, pod := range podsCopy {
			select {
			case <-cleanupCtx.Done():
				k.logger.Info("Pod cleanup context cancelled, stopping cleanup")
				return
			default:
			}

			if err := k.deleteRunnerPod(cleanupCtx, &pod); err != nil && !k8serrors.IsNotFound(err) {
				k.logger.Error(
					err,
					"Failed to cleanup terminated pod",
					"podName", pod.Name,
					"phase", pod.Status.Phase,
				)
			} else {
				metrics.RunnerPodLifecycle.WithLabelValues(
					k.namespace,
					fmt.Sprintf("%d", k.scaleSetID),
					k.runnerGroup,
					"cleaned_up",
				).Inc()
			}
		}
	}()
}

func (k *KubernetesScaler) filterIdlePods(
	pods []corev1.Pod,
) []corev1.Pod {
	var idlePods []corev1.Pod
	for i := range pods {
		if pods[i].Labels[constants.LabelRunnerState] == constants.RunnerStateIdle {
			idlePods = append(idlePods, pods[i])
		}
	}
	return idlePods
}

func (k *KubernetesScaler) findRunnerPod(
	ctx context.Context,
	runnerName string,
) (*corev1.Pod, error) {
	podList := &corev1.PodList{}

	if err := k.k8sClient.List(
		ctx,
		podList,
		k8sclient.InNamespace(k.namespace),
		k8sclient.MatchingFields{
			"metadata.labels." + constants.LabelRunnerName: runnerName,
		},
	); err != nil {
		return nil, err
	}

	if len(podList.Items) == 0 {
		return nil, nil
	}

	return &podList.Items[0], nil
}

func (k *KubernetesScaler) createRunnerPod(
	ctx context.Context,
) error {
	startTime := time.Now()
	runnerName := fmt.Sprintf("runner-%s", uuid.NewString()[:8])

	if err := k.waitForRateLimit(ctx); err != nil {
		return fmt.Errorf("rate limit wait cancelled: %w", err)
	}

	jitConfig, err := k.generateJITConfig(ctx, runnerName, startTime)
	if err != nil {
		return err
	}

	podLabels := k.buildPodLabels(runnerName)
	podAnnotations := k.buildPodAnnotations(jitConfig.EncodedJITConfig)
	envVars := k.buildEnvVars(jitConfig.EncodedJITConfig)
	podSpec := k.buildRunnerPodSpec(envVars)

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: fmt.Sprintf("runner-%s-", runnerName),
			Namespace:    k.namespace,
			Labels:       podLabels,
			Annotations:  podAnnotations,
		},
		Spec: podSpec,
	}

	if k.ownerRef != nil {
		pod.OwnerReferences = []metav1.OwnerReference{*k.ownerRef}
	}

	if err := k.k8sClient.Create(ctx, pod); err != nil {
		metrics.RunnerPodCreationDuration.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			"error",
		).Observe(time.Since(startTime).Seconds())
		return fmt.Errorf("failed to create pod: %w", err)
	}

	metrics.RunnerPodCreationDuration.WithLabelValues(
		k.namespace,
		fmt.Sprintf("%d", k.scaleSetID),
		k.runnerGroup,
		"success",
	).Observe(time.Since(startTime).Seconds())
	metrics.RunnerPodLifecycle.WithLabelValues(
		k.namespace,
		fmt.Sprintf("%d", k.scaleSetID),
		k.runnerGroup,
		"created",
	).Inc()

	k.logger.Info("Created runner pod", "podName", pod.Name, "runnerName", runnerName)
	return nil
}

func (k *KubernetesScaler) generateJITConfig(
	ctx context.Context,
	runnerName string,
	startTime time.Time,
) (*scaleset.RunnerScaleSetJitRunnerConfig, error) {
	jitStartTime := time.Now()
	jitConfig, err := k.scalesetClient.GenerateJitRunnerConfig(
		ctx,
		&scaleset.RunnerScaleSetJitRunnerSetting{Name: runnerName},
		k.scaleSetID,
	)
	jitDuration := time.Since(jitStartTime).Seconds()

	if err != nil {
		metrics.JITConfigGeneration.WithLabelValues(
			k.namespace, fmt.Sprintf("%d", k.scaleSetID), k.runnerGroup, "error",
		).Inc()
		metrics.JITConfigGenerationDuration.WithLabelValues(
			k.namespace, fmt.Sprintf("%d", k.scaleSetID), k.runnerGroup,
		).Observe(jitDuration)
		metrics.RunnerPodCreationDuration.WithLabelValues(
			k.namespace, fmt.Sprintf("%d", k.scaleSetID), k.runnerGroup, "error",
		).Observe(time.Since(startTime).Seconds())
		return nil, fmt.Errorf("failed to generate JIT config: %w", err)
	}

	metrics.JITConfigGeneration.WithLabelValues(
		k.namespace, fmt.Sprintf("%d", k.scaleSetID), k.runnerGroup, "success",
	).Inc()
	metrics.JITConfigGenerationDuration.WithLabelValues(
		k.namespace, fmt.Sprintf("%d", k.scaleSetID), k.runnerGroup,
	).Observe(jitDuration)
	return jitConfig, nil
}

func (k *KubernetesScaler) buildPodLabels(runnerName string) map[string]string {
	podLabels := map[string]string{
		constants.LabelRunnerName:   runnerName,
		constants.LabelRunnerState:  constants.RunnerStateIdle,
		constants.LabelAutoscaleSet: fmt.Sprintf("%d", k.scaleSetID),
	}
	if k.runnerGroup != "" {
		podLabels[constants.LabelRunnerGroup] = k.runnerGroup
	}
	if k.template == nil || len(k.template.Metadata.Labels) == 0 {
		return podLabels
	}
	reservedLabels := map[string]bool{
		constants.LabelRunnerName: true, constants.LabelRunnerState: true,
		constants.LabelAutoscaleSet: true, constants.LabelRunnerGroup: true,
	}
	for labelKey, labelValue := range k.template.Metadata.Labels {
		if reservedLabels[labelKey] {
			k.logger.Info("Skipping reserved label from template", "label", labelKey, "reason", "reserved for system use")
			continue
		}
		podLabels[labelKey] = labelValue
	}
	return podLabels
}

func (k *KubernetesScaler) buildPodAnnotations(jitConfigEncoded string) map[string]string {
	podAnnotations := map[string]string{constants.AnnotationJITConfig: jitConfigEncoded}
	if k.template != nil && len(k.template.Metadata.Annotations) > 0 {
		for annKey, annVal := range k.template.Metadata.Annotations {
			podAnnotations[annKey] = annVal
		}
	}
	return podAnnotations
}

func (k *KubernetesScaler) buildEnvVars(jitConfigEncoded string) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{Name: "ACTIONS_RUNNER_INPUT_JITCONFIG", Value: jitConfigEncoded},
	}
	if k.template != nil && k.template.Spec.Env != nil {
		for _, env := range k.template.Spec.Env {
			envVars = append(envVars, corev1.EnvVar{Name: env.Name, Value: env.Value})
		}
	}
	return envVars
}

func (k *KubernetesScaler) buildRunnerPodSpec(envVars []corev1.EnvVar) corev1.PodSpec {
	podSpec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		Containers: []corev1.Container{
			{
				Name:    "runner",
				Image:   k.runnerImage,
				Command: []string{"/home/runner/run.sh"},
				Env:     envVars,
			},
		},
	}

	if k.template != nil && k.template.Spec.Resources.Limits != nil {
		limits := make(corev1.ResourceList)
		for resKey, resVal := range k.template.Spec.Resources.Limits {
			if qty, err := resource.ParseQuantity(resVal); err == nil {
				limits[corev1.ResourceName(resKey)] = qty
			}
		}
		if len(limits) > 0 {
			podSpec.Containers[0].Resources.Limits = limits
		}
	}
	if k.template != nil && k.template.Spec.Resources.Requests != nil {
		requests := make(corev1.ResourceList)
		for resKey, resVal := range k.template.Spec.Resources.Requests {
			if qty, err := resource.ParseQuantity(resVal); err == nil {
				requests[corev1.ResourceName(resKey)] = qty
			}
		}
		if len(requests) > 0 {
			podSpec.Containers[0].Resources.Requests = requests
		}
	}
	if k.template != nil && len(k.template.Spec.NodeSelector) > 0 {
		podSpec.NodeSelector = k.template.Spec.NodeSelector
	}
	if k.template != nil && len(k.template.Spec.Tolerations) > 0 {
		tolerations := make([]corev1.Toleration, 0, len(k.template.Spec.Tolerations))
		for _, t := range k.template.Spec.Tolerations {
			op := corev1.TolerationOperator(t.Operator)
			if op == "" {
				op = corev1.TolerationOpEqual
			}
			tolerations = append(tolerations, corev1.Toleration{
				Key: t.Key, Operator: op, Value: t.Value, Effect: corev1.TaintEffect(t.Effect),
			})
		}
		podSpec.Tolerations = tolerations
	}

	uid, gid := int64(1001), int64(1001)
	podSpec.SecurityContext = &corev1.PodSecurityContext{
		RunAsUser: &uid, RunAsGroup: &gid,
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
	allowEscalation := false
	readOnlyRoot := false
	podSpec.Containers[0].SecurityContext = &corev1.SecurityContext{
		AllowPrivilegeEscalation: &allowEscalation,
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		ReadOnlyRootFilesystem:   &readOnlyRoot,
	}
	return podSpec
}

func (k *KubernetesScaler) batchCreateRunnerPods(
	ctx context.Context,
	count int,
) int {
	var created atomic.Int32
	var errCount atomic.Int32

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(constants.BatchMaxWorkers)

	for i := 0; i < count; i++ {
		// Stop launching new work when errors >= 50% of total attempts so far
		currentErrors := int(errCount.Load())
		currentCreated := int(created.Load())
		totalAttempted := currentErrors + currentCreated
		if totalAttempted > 0 && currentErrors >= totalAttempted/2 {
			k.logger.Info(
				"Stopping batch creation due to error threshold",
				"created", currentCreated,
				"requested", count,
				"errors", currentErrors,
			)
			break
		}

		g.Go(func() error {
			if err := k.createRunnerPod(gctx); err != nil {
				errCount.Add(1)
				k.logger.Error(
					err,
					"Failed to create runner pod in batch",
					"created", created.Load(),
					"requested", count,
				)
				return nil // don't cancel the group
			}
			created.Add(1)
			return nil
		})
	}

	_ = g.Wait()
	return int(created.Load())
}

func (k *KubernetesScaler) removeRunnerFromGitHub(
	ctx context.Context,
	runnerName string,
) error {
	if err := k.waitForRateLimit(ctx); err != nil {
		return fmt.Errorf("rate limit wait cancelled: %w", err)
	}

	var runner *scaleset.RunnerReference
	err := client.RetryWithBackoff(ctx, client.DefaultRetryConfig, func() error {
		var getErr error
		runner, getErr = k.scalesetClient.GetRunnerByName(ctx, runnerName)
		return getErr
	})
	if err != nil {
		return fmt.Errorf("failed to get runner by name: %w", err)
	}

	if runner == nil {
		k.logger.Info(
			"Runner not found in GitHub, may have already been removed",
			"runnerName", runnerName,
		)
		return nil
	}

	if err = k.waitForRateLimit(ctx); err != nil {
		return fmt.Errorf("rate limit wait cancelled: %w", err)
	}

	err = client.RetryWithBackoff(ctx, client.DefaultRetryConfig, func() error {
		return k.scalesetClient.RemoveRunner(ctx, int64(runner.ID))
	})
	if err != nil {
		return fmt.Errorf("failed to remove runner from GitHub: %w", err)
	}

	k.logger.Info(
		"Removed runner from GitHub",
		"runnerName", runnerName,
		"runnerID", runner.ID,
	)

	return nil
}

func (k *KubernetesScaler) deleteRunnerPod(
	ctx context.Context,
	pod *corev1.Pod,
) error {
	gracePeriod := int64(constants.DeleteGracePeriodSeconds)
	deleteOptions := k8sclient.DeleteOptions{
		GracePeriodSeconds: &gracePeriod,
	}

	if err := k.k8sClient.Delete(ctx, pod, &deleteOptions); err != nil {
		if k8serrors.IsNotFound(err) {
			k.logger.Info(
				"Pod already deleted",
				"podName", pod.Name,
			)
			return nil
		}
		return err
	}

	// Best-effort GitHub cleanup in background; reconcile context would cancel before cleanup completes.
	runnerName := pod.Labels[constants.LabelRunnerName]
	if runnerName != "" {
		go func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), constants.CleanupTimeout)
			defer cancel()
			if err := k.removeRunnerFromGitHub(cleanupCtx, runnerName); err != nil {
				k.logger.Error(
					err,
					"Failed to remove runner from GitHub after pod deletion",
					"podName", pod.Name,
					"runnerName", runnerName,
				)
			}
		}()
	}

	return nil
}

func (k *KubernetesScaler) batchDeleteRunnerPods(
	ctx context.Context,
	pods []corev1.Pod,
) int {
	var deleted atomic.Int32
	var errCount atomic.Int32

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(constants.BatchMaxWorkers)

	for i := range pods {
		pod := &pods[i]

		// Stop launching new work when errors >= 50% of total attempts
		currentErrors := int(errCount.Load())
		currentDeleted := int(deleted.Load())
		totalAttempted := currentErrors + currentDeleted
		if totalAttempted > 0 && currentErrors >= totalAttempted/2 {
			k.logger.Info(
				"Stopping batch deletion due to error threshold",
				"deleted", currentDeleted,
				"requested", len(pods),
				"errors", currentErrors,
			)
			break
		}

		g.Go(func() error {
			if err := k.deleteRunnerPod(gctx, pod); err != nil {
				if k8serrors.IsNotFound(err) {
					deleted.Add(1)
					return nil
				}
				errCount.Add(1)
				k.logger.Error(
					err,
					"Failed to delete runner pod in batch",
					"pod", pod.Name,
					"deleted", deleted.Load(),
					"requested", len(pods),
				)
				return nil // don't cancel the group
			}
			deleted.Add(1)
			return nil
		})
	}

	_ = g.Wait()
	return int(deleted.Load())
}
