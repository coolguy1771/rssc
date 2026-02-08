# HighJobQueueTime

## Summary

Jobs are spending too long in queue (P95 > 5 minutes over 10m).

## Impact

Workflow runs wait longer for a runner; user experience and SLA may be affected.

## Steps

1. Check current runner counts and scaling limits: `kubectl get runnersets -A -o custom-columns='NS:.metadata.namespace,NAME:.metadata.name,GROUP:.spec.runnerGroup,TOTAL:.status.runnerCount,IDLE:.status.idleRunners,MAX:.spec...'` (and AutoscaleSet maxRunners)
2. Review scaling constraints metric: `scaleset_scaling_constraints_total` to see if scale-up is hitting max_runners
3. Consider increasing maxRunners on the AutoscaleSet or adding more RunnerSets for the same group
4. Check for pod creation failures (RunnerPodCreationFailures runbook) or listener/session issues that prevent scaling up
