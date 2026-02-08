package scaler

import (
	"context"
	"fmt"
	"time"

	"github.com/actions/scaleset"
	"github.com/coolguy1771/rssc/internal/client"
	"github.com/coolguy1771/rssc/internal/constants"
	"github.com/coolguy1771/rssc/internal/metrics"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func (k *KubernetesScaler) getRunnerPods(ctx context.Context) ([]corev1.Pod, error) {
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
