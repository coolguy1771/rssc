package scaler

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/actions/scaleset"
	"github.com/coolguy1771/rssc/internal/constants"
	"github.com/coolguy1771/rssc/internal/metrics"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (k *KubernetesScaler) createRunnerPod(ctx context.Context) error {
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

func (k *KubernetesScaler) batchCreateRunnerPods(ctx context.Context, count int) int {
	var created atomic.Int32
	var errCount atomic.Int32
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(constants.BatchMaxWorkers)
	for i := 0; i < count; i++ {
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
				return nil
			}
			created.Add(1)
			return nil
		})
	}
	_ = g.Wait()
	return int(created.Load())
}
