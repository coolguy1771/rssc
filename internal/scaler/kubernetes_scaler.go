package scaler

import (
	"context"
	"fmt"
	"time"

	"github.com/actions/scaleset"
	scalesetv1alpha1 "github.com/coolguy1771/rssc/api/v1alpha1"
	"github.com/coolguy1771/rssc/internal/client"
	"github.com/coolguy1771/rssc/internal/metrics"
	"github.com/go-logr/logr"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	LabelRunnerName     = "scaleset.actions.github.com/runner-name"
	LabelRunnerState    = "scaleset.actions.github.com/runner-state"
	LabelAutoscaleSet   = "scaleset.actions.github.com/autoscaleset"
	LabelRunnerGroup    = "scaleset.actions.github.com/runner-group"
	AnnotationJITConfig = "scaleset.actions.github.com/jit-config"
	RunnerStateIdle     = "idle"
	RunnerStateBusy     = "busy"
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
		logger:         logger,
	}
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
		scaleUp := targetCount - currentCount
		requestedScaleUp := scaleUp
		if targetCount == k.maxRunners && currentCount < k.maxRunners {
			metrics.ScalingConstraints.WithLabelValues(
				k.namespace,
				fmt.Sprintf("%d", k.scaleSetID),
				k.runnerGroup,
				"max_runners",
			).Inc()
		}
		created := k.batchCreateRunnerPods(ctx, scaleUp)
		if created < requestedScaleUp {
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

	if targetCount < currentCount {
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

	return currentCount, nil
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

	oldState := pod.Labels[LabelRunnerState]
	pod.Labels[LabelRunnerState] = RunnerStateBusy
	if err := k.k8sClient.Update(ctx, pod); err != nil {
		return fmt.Errorf("failed to update runner pod: %w", err)
	}

	if oldState != RunnerStateBusy {
		metrics.RunnerStateTransitions.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			oldState,
			RunnerStateBusy,
		).Inc()
	}

	metrics.JobLifecycle.WithLabelValues(
		k.namespace,
		fmt.Sprintf("%d", k.scaleSetID),
		k.runnerGroup,
		"started",
		"success",
	).Inc()

	if jobInfo.RepositoryName != "" {
		metrics.JobRepository.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			jobInfo.RepositoryName,
			"started",
		).Inc()
	}

	if jobInfo.JobWorkflowRef != "" {
		repo := jobInfo.RepositoryName
		if repo == "" {
			repo = "unknown"
		}
		metrics.JobWorkflow.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			repo,
			jobInfo.JobWorkflowRef,
			"started",
		).Inc()
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

	if jobInfo.RepositoryName != "" {
		metrics.JobRepository.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			jobInfo.RepositoryName,
			"completed",
		).Inc()
	}

	if jobInfo.JobWorkflowRef != "" {
		repo := jobInfo.RepositoryName
		if repo == "" {
			repo = "unknown"
		}
		metrics.JobWorkflow.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			repo,
			jobInfo.JobWorkflowRef,
			"completed",
		).Inc()
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

	if err := k.removeRunnerFromGitHub(ctx, jobInfo.RunnerName); err != nil {
		k.logger.Error(
			err,
			"Failed to remove runner from GitHub",
			"runnerName", jobInfo.RunnerName,
		)
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
	selector := labels.SelectorFromSet(labels.Set{
		LabelAutoscaleSet: fmt.Sprintf("%d", k.scaleSetID),
	})

	if err := k.k8sClient.List(
		ctx,
		podList,
		k8sclient.InNamespace(k.namespace),
		k8sclient.MatchingLabelsSelector{Selector: selector},
	); err != nil {
		return nil, err
	}

	var activePods []corev1.Pod
	var podsToCleanup []corev1.Pod

	for _, pod := range podList.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}

		phase := pod.Status.Phase
		if phase == corev1.PodSucceeded || phase == corev1.PodFailed {
			podsToCleanup = append(podsToCleanup, pod)
			continue
		}

		if phase == corev1.PodPending {
			if time.Since(pod.CreationTimestamp.Time) > 5*time.Minute {
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

	if len(podsToCleanup) > 0 {
		podsCopy := make([]corev1.Pod, len(podsToCleanup))
		copy(podsCopy, podsToCleanup)
		go func() {
			cleanupCtx, cancel := context.WithTimeout(
				context.Background(),
				30*time.Second,
			)
			defer cancel()

			for _, pod := range podsCopy {
				select {
				case <-cleanupCtx.Done():
					k.logger.Info(
						"Pod cleanup context cancelled, stopping cleanup",
					)
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

	return activePods, nil
}

func (k *KubernetesScaler) filterIdlePods(
	pods []corev1.Pod,
) []corev1.Pod {
	var idlePods []corev1.Pod
	for i := range pods {
		if pods[i].Labels[LabelRunnerState] == RunnerStateIdle {
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
	selector := labels.SelectorFromSet(labels.Set{
		LabelRunnerName: runnerName,
	})

	if err := k.k8sClient.List(
		ctx,
		podList,
		k8sclient.InNamespace(k.namespace),
		k8sclient.MatchingLabelsSelector{Selector: selector},
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

	jitStartTime := time.Now()
	jitConfig, err := k.scalesetClient.GenerateJitRunnerConfig(
		ctx,
		&scaleset.RunnerScaleSetJitRunnerSetting{
			Name: runnerName,
		},
		k.scaleSetID,
	)
	jitDuration := time.Since(jitStartTime).Seconds()

	if err != nil {
		metrics.JITConfigGeneration.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			"error",
		).Inc()
		metrics.JITConfigGenerationDuration.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
		).Observe(jitDuration)
		metrics.RunnerPodCreationDuration.WithLabelValues(
			k.namespace,
			fmt.Sprintf("%d", k.scaleSetID),
			k.runnerGroup,
			"error",
		).Observe(time.Since(startTime).Seconds())
		return fmt.Errorf("failed to generate JIT config: %w", err)
	}

	metrics.JITConfigGeneration.WithLabelValues(
		k.namespace,
		fmt.Sprintf("%d", k.scaleSetID),
		k.runnerGroup,
		"success",
	).Inc()
	metrics.JITConfigGenerationDuration.WithLabelValues(
		k.namespace,
		fmt.Sprintf("%d", k.scaleSetID),
		k.runnerGroup,
	).Observe(jitDuration)

	// Set system labels first - these are required for the operator
	// to manage runner pods correctly
	podLabels := map[string]string{
		LabelRunnerName:   runnerName,
		LabelRunnerState:  RunnerStateIdle,
		LabelAutoscaleSet: fmt.Sprintf("%d", k.scaleSetID),
	}
	if k.runnerGroup != "" {
		podLabels[LabelRunnerGroup] = k.runnerGroup
	}

	podAnnotations := map[string]string{
		AnnotationJITConfig: jitConfig.EncodedJITConfig,
	}

	// Merge user-provided labels from template, but protect reserved labels
	if k.template != nil {
		if len(k.template.Metadata.Labels) > 0 {
			reservedLabels := map[string]bool{
				LabelRunnerName:   true,
				LabelRunnerState:  true,
				LabelAutoscaleSet: true,
				LabelRunnerGroup:  true,
			}
			for labelKey, labelValue := range k.template.Metadata.Labels {
				if !reservedLabels[labelKey] {
					podLabels[labelKey] = labelValue
				} else {
					k.logger.Info(
						"Skipping reserved label from template",
						"label", labelKey,
						"reason", "reserved for system use",
					)
				}
			}
		}
		if len(k.template.Metadata.Annotations) > 0 {
			for k, v := range k.template.Metadata.Annotations {
				podAnnotations[k] = v
			}
		}
	}

	envVars := []corev1.EnvVar{
		{
			Name:  "ACTIONS_RUNNER_INPUT_JITCONFIG",
			Value: jitConfig.EncodedJITConfig,
		},
	}

	if k.template != nil && k.template.Spec.Env != nil {
		for _, env := range k.template.Spec.Env {
			envVars = append(envVars, corev1.EnvVar{
				Name:  env.Name,
				Value: env.Value,
			})
		}
	}

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
		for k, v := range k.template.Spec.Resources.Limits {
			qty, err := resource.ParseQuantity(v)
			if err == nil {
				limits[corev1.ResourceName(k)] = qty
			}
		}
		if len(limits) > 0 {
			podSpec.Containers[0].Resources.Limits = limits
		}
	}

	if k.template != nil && k.template.Spec.Resources.Requests != nil {
		requests := make(corev1.ResourceList)
		for k, v := range k.template.Spec.Resources.Requests {
			qty, err := resource.ParseQuantity(v)
			if err == nil {
				requests[corev1.ResourceName(k)] = qty
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
			toleration := corev1.Toleration{
				Key:      t.Key,
				Operator: corev1.TolerationOperator(t.Operator),
				Value:    t.Value,
				Effect:   corev1.TaintEffect(t.Effect),
			}
			if toleration.Operator == "" {
				toleration.Operator = corev1.TolerationOpEqual
			}
			tolerations = append(tolerations, toleration)
		}
		podSpec.Tolerations = tolerations
	}

	podSpec.SecurityContext = &corev1.PodSecurityContext{
		RunAsUser:  func() *int64 { uid := int64(1001); return &uid }(),
		RunAsGroup: func() *int64 { gid := int64(1001); return &gid }(),
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}

	podSpec.Containers[0].SecurityContext = &corev1.SecurityContext{
		AllowPrivilegeEscalation: func() *bool { b := false; return &b }(),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		ReadOnlyRootFilesystem: func() *bool { b := false; return &b }(),
	}

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

	k.logger.Info(
		"Created runner pod",
		"podName", pod.Name,
		"runnerName", runnerName,
	)

	return nil
}

func (k *KubernetesScaler) batchCreateRunnerPods(
	ctx context.Context,
	count int,
) int {
	const batchSize = 10
	created := 0
	errors := 0

	for i := 0; i < count; i++ {
		if errors > 0 && errors >= batchSize/2 {
			k.logger.Info(
				"Stopping batch creation due to errors",
				"created", created,
				"requested", count,
				"errors", errors,
			)
			break
		}

		if err := k.createRunnerPod(ctx); err != nil {
			errors++
			k.logger.Error(
				err,
				"Failed to create runner pod in batch",
				"created", created,
				"requested", count,
			)
			continue
		}
		created++

		if (i+1)%batchSize == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	return created
}

func (k *KubernetesScaler) removeRunnerFromGitHub(
	ctx context.Context,
	runnerName string,
) error {
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
	runnerName := pod.Labels[LabelRunnerName]
	if runnerName != "" {
		if err := k.removeRunnerFromGitHub(ctx, runnerName); err != nil {
			k.logger.Error(
				err,
				"Failed to remove runner from GitHub before pod deletion",
				"podName", pod.Name,
				"runnerName", runnerName,
			)
		}
	}

	gracePeriod := int64(30)
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

	return nil
}

func (k *KubernetesScaler) batchDeleteRunnerPods(
	ctx context.Context,
	pods []corev1.Pod,
) int {
	const batchSize = 10
	deleted := 0
	errors := 0

	for i, pod := range pods {
		if errors > 0 && errors >= batchSize/2 {
			k.logger.Info(
				"Stopping batch deletion due to errors",
				"deleted", deleted,
				"requested", len(pods),
				"errors", errors,
			)
			break
		}

		if err := k.deleteRunnerPod(ctx, &pod); err != nil {
			if k8serrors.IsNotFound(err) {
				deleted++
				continue
			}
			errors++
			k.logger.Error(
				err,
				"Failed to delete runner pod in batch",
				"pod", pod.Name,
				"deleted", deleted,
				"requested", len(pods),
			)
			continue
		}
		deleted++

		if (i+1)%batchSize == 0 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	return deleted
}
