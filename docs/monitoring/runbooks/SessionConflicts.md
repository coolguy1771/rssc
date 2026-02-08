# SessionConflicts

## Summary

Multiple message session conflicts detected (rate > 0.1/s over 5m). Usually indicates more than one controller instance claiming the same scale set or restarts before the previous session expired.

## Impact

RunnerSet may fail to start the listener; scaling may be delayed until the previous session expires on GitHub's side.

## Steps

1. Ensure only one controller replica when leader election is enabled, or that each replica manages disjoint RunnerSets
2. Check for frequent restarts: `kubectl get pods -n system -l app.kubernetes.io/name=scaleset-controller -o wide` and creation timestamps
3. Review session conflict metrics labels (namespace, autoscaleset, runner group, hostname) to identify which RunnerSet is affected
4. If a single controller is intended, increase leader election lease or reduce restart frequency; allow time for GitHub to expire the old session (typically a few minutes)
