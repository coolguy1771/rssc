package scaler

import (
	"context"
	"fmt"

	"github.com/actions/scaleset"
	scalesetv1alpha1 "github.com/coolguy1771/rssc/api/v1alpha1"
	"github.com/coolguy1771/rssc/internal/client"
	"github.com/coolguy1771/rssc/internal/metrics"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
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
