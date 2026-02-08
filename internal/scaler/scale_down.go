package scaler

import (
	"context"
	"sync/atomic"

	"github.com/coolguy1771/rssc/internal/constants"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
)

func (k *KubernetesScaler) filterIdlePods(pods []corev1.Pod) []corev1.Pod {
	var idlePods []corev1.Pod
	for i := range pods {
		if pods[i].Labels[constants.LabelRunnerState] == constants.RunnerStateIdle {
			idlePods = append(idlePods, pods[i])
		}
	}
	return idlePods
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
				return nil
			}
			deleted.Add(1)
			return nil
		})
	}
	_ = g.Wait()
	return int(deleted.Load())
}
