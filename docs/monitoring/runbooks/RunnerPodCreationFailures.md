# RunnerPodCreationFailures

## Summary

High rate of runner pod creation failures (created-with-error in pod lifecycle metrics).

## Impact

Fewer runners available than needed; jobs may queue longer or fail to get a runner.

## Steps

1. Check controller logs for create errors: `kubectl logs -n system -l app.kubernetes.io/name=scaleset-controller --tail=500` and look for "failed to create" or "Create pod"
2. Inspect events in the namespace where RunnerSet creates pods: `kubectl get events -n <runner-ns> --sort-by='.lastTimestamp'`
3. Common causes: ResourceQuota or LimitRange in namespace blocking pod creation, image pull errors, RBAC missing for the controller to create pods in that namespace, node constraints (taints, capacity)
4. Verify controller has RBAC to create pods in the target namespace and that quota/limits allow the runner pod spec (including resource requests if set)
