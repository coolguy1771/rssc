package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	AutoscaleSetTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "scaleset_autoscaleset_total",
			Help: "Total number of AutoscaleSet resources",
		},
		[]string{"namespace", "phase"},
	)

	RunnerSetTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "scaleset_runnerset_total",
			Help: "Total number of RunnerSet resources",
		},
		[]string{"namespace", "status"},
	)

	RunnerPodsTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "scaleset_runner_pods_total",
			Help: "Total number of runner pods",
		},
		[]string{"namespace", "autoscaleset", "runner_group", "state"},
	)

	RunnerPodsIdle = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "scaleset_runner_pods_idle",
			Help: "Number of idle runner pods",
		},
		[]string{"namespace", "autoscaleset", "runner_group"},
	)

	RunnerPodsBusy = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "scaleset_runner_pods_busy",
			Help: "Number of busy runner pods",
		},
		[]string{"namespace", "autoscaleset", "runner_group"},
	)

	ScaleSetOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_operations_total",
			Help: "Total number of scale set operations",
		},
		[]string{"namespace", "autoscaleset", "operation", "status"},
	)

	ListenerStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "scaleset_listener_active",
			Help: "Whether a listener is active (1) or not (0)",
		},
		[]string{"namespace", "autoscaleset", "runner_group"},
	)

	ReconciliationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "scaleset_reconciliation_duration_seconds",
			Help:    "Duration of reconciliation operations in seconds",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 10),
		},
		[]string{"controller", "namespace", "name"},
	)

	RunnerPodLifecycle = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_runner_pod_lifecycle_total",
			Help: "Total number of runner pod lifecycle events",
		},
		[]string{"namespace", "autoscaleset", "runner_group", "event"},
	)

	RunnerPodStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "scaleset_runner_pod_status",
			Help: "Number of runner pods by Kubernetes phase status",
		},
		[]string{"namespace", "autoscaleset", "runner_group", "phase"},
	)

	RunnerScalingOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_runner_scaling_operations_total",
			Help: "Total number of runner scaling operations",
		},
		[]string{"namespace", "autoscaleset", "runner_group", "operation", "result"},
	)

	RunnerScalingAmount = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "scaleset_runner_scaling_amount",
			Help:    "Number of runners scaled in a single operation",
			Buckets: []float64{1, 2, 5, 10, 20, 50, 100},
		},
		[]string{"namespace", "autoscaleset", "runner_group", "operation"},
	)

	RunnerStateTransitions = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_runner_state_transitions_total",
			Help: "Total number of runner state transitions",
		},
		[]string{"namespace", "autoscaleset", "runner_group", "from_state", "to_state"},
	)

	JobLifecycle = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_job_lifecycle_total",
			Help: "Total number of job lifecycle events",
		},
		[]string{"namespace", "autoscaleset", "runner_group", "event", "result"},
	)

	JobQueueTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "scaleset_job_queue_time_seconds",
			Help:    "Time jobs spend in queue before being assigned",
			Buckets: prometheus.ExponentialBuckets(1, 2, 10),
		},
		[]string{"namespace", "autoscaleset", "runner_group"},
	)

	JobExecutionTime = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "scaleset_job_execution_time_seconds",
			Help:    "Time jobs take to execute",
			Buckets: prometheus.ExponentialBuckets(10, 2, 12),
		},
		[]string{"namespace", "autoscaleset", "runner_group", "result"},
	)

	QueuedJobs = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "scaleset_queued_jobs",
			Help: "Current number of queued jobs waiting for runners",
		},
		[]string{"namespace", "autoscaleset", "runner_group"},
	)

	RunnerPodAge = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "scaleset_runner_pod_age_seconds",
			Help:    "Age of runner pods in seconds",
			Buckets: prometheus.ExponentialBuckets(60, 2, 12),
		},
		[]string{"namespace", "autoscaleset", "runner_group", "state"},
	)

	RunnerPodCreationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "scaleset_runner_pod_creation_duration_seconds",
			Help:    "Time taken to create a runner pod",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 10),
		},
		[]string{"namespace", "autoscaleset", "runner_group", "result"},
	)

	RunnerPodDeletionDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "scaleset_runner_pod_deletion_duration_seconds",
			Help:    "Time taken to delete a runner pod",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 10),
		},
		[]string{"namespace", "autoscaleset", "runner_group", "result"},
	)

	ScaleSetStatistics = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "scaleset_statistics",
			Help: "Scale set statistics from GitHub API",
		},
		[]string{"namespace", "autoscaleset", "statistic"},
	)

	ScaleSetLimits = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "scaleset_limits",
			Help: "Scale set min and max runner limits",
		},
		[]string{"namespace", "autoscaleset", "limit_type"},
	)

	SessionOperations = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_session_operations_total",
			Help: "Total number of message session operations",
		},
		[]string{"namespace", "autoscaleset", "runner_group", "operation", "result"},
	)

	SessionConflicts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_session_conflicts_total",
			Help: "Total number of session conflicts (409 errors)",
		},
		[]string{"namespace", "autoscaleset", "runner_group", "owner"},
	)

	JITConfigGeneration = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_jit_config_generation_total",
			Help: "Total number of JIT config generation operations",
		},
		[]string{"namespace", "autoscaleset", "runner_group", "result"},
	)

	JITConfigGenerationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "scaleset_jit_config_generation_duration_seconds",
			Help:    "Time taken to generate JIT config",
			Buckets: prometheus.ExponentialBuckets(0.01, 2, 10),
		},
		[]string{"namespace", "autoscaleset", "runner_group"},
	)

	ListenerMessagesProcessed = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_listener_messages_processed_total",
			Help: "Total number of messages processed by listener",
		},
		[]string{"namespace", "autoscaleset", "runner_group", "message_type"},
	)

	ListenerMessageErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_listener_message_errors_total",
			Help: "Total number of message processing errors",
		},
		[]string{"namespace", "autoscaleset", "runner_group", "error_type"},
	)

	ScalingConstraints = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_scaling_constraints_total",
			Help: "Total number of times scaling was constrained by limits",
		},
		[]string{"namespace", "autoscaleset", "runner_group", "constraint_type"},
	)

	GitHubAPICallDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "scaleset_github_api_call_duration_seconds",
			Help:    "Duration of GitHub API calls",
			Buckets: prometheus.ExponentialBuckets(0.01, 2, 10),
		},
		[]string{"namespace", "autoscaleset", "operation", "status"},
	)

	GitHubAPICallErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_github_api_call_errors_total",
			Help: "Total number of GitHub API call errors",
		},
		[]string{"namespace", "autoscaleset", "operation", "status_code", "error_type"},
	)

	JobRepository = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_job_repository_total",
			Help: "Total number of jobs by repository",
		},
		[]string{"namespace", "autoscaleset", "runner_group", "repository", "event"},
	)

	JobWorkflow = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_job_workflow_total",
			Help: "Total number of jobs by workflow",
		},
		[]string{"namespace", "autoscaleset", "runner_group", "repository", "workflow", "event"},
	)

	ReconciliationQueueDepth = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "scaleset_reconciliation_queue_depth",
			Help: "Current depth of reconciliation queue",
		},
		[]string{"controller"},
	)

	ReconciliationRate = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_reconciliation_rate_total",
			Help: "Total number of reconciliations",
		},
		[]string{"controller", "result"},
	)

	CacheHits = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_cache_hits_total",
			Help: "Total number of cache hits",
		},
		[]string{"cache_type"},
	)

	CacheMisses = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_cache_misses_total",
			Help: "Total number of cache misses",
		},
		[]string{"cache_type"},
	)

	RateLimitHits = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "scaleset_rate_limit_hits_total",
			Help: "Total number of rate limit hits",
		},
		[]string{"operation"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		AutoscaleSetTotal,
		RunnerSetTotal,
		RunnerPodsTotal,
		RunnerPodsIdle,
		RunnerPodsBusy,
		ScaleSetOperations,
		ListenerStatus,
		ReconciliationDuration,
		RunnerPodLifecycle,
		RunnerPodStatus,
		RunnerScalingOperations,
		RunnerScalingAmount,
		RunnerStateTransitions,
		JobLifecycle,
		JobQueueTime,
		JobExecutionTime,
		QueuedJobs,
		RunnerPodAge,
		RunnerPodCreationDuration,
		RunnerPodDeletionDuration,
		ScaleSetStatistics,
		ScaleSetLimits,
		SessionOperations,
		SessionConflicts,
		JITConfigGeneration,
		JITConfigGenerationDuration,
		ListenerMessagesProcessed,
		ListenerMessageErrors,
		ScalingConstraints,
		GitHubAPICallDuration,
		GitHubAPICallErrors,
		JobRepository,
		JobWorkflow,
		ReconciliationQueueDepth,
		ReconciliationRate,
		CacheHits,
		CacheMisses,
		RateLimitHits,
	)
}
