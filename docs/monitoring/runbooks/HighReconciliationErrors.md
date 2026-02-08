# HighReconciliationErrors

## Summary

The controller is experiencing a high rate of reconciliation errors (rate > 0.1/s over 5m).

## Impact

AutoscaleSet or RunnerSet resources may be stuck in error state; status conditions will show failure reasons.

## Steps

1. Check controller logs for recent errors: `kubectl logs -n system -l app.kubernetes.io/name=scaleset-controller --tail=500 | grep -i error`
2. Inspect CR status: `kubectl get autoscalesets,runnersets -A -o wide` and `kubectl describe autoscaleset -n <ns> <name>` (and runnerset)
3. Common causes: missing or invalid Secret (GitHub App / PAT), GitHub API rate limits, invalid AutoscaleSet ref on RunnerSet, RBAC or API server issues
4. Fix underlying cause (e.g. correct secret, increase rate limits, fix CR spec) and allow requeue or restart controller if needed
