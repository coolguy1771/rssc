package scaler

import (
	"context"
	"fmt"

	"github.com/actions/scaleset"
	"github.com/coolguy1771/rssc/internal/constants"
	"github.com/coolguy1771/rssc/internal/metrics"
)

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
