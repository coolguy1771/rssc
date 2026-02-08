# ScalesetControllerDown

## Summary

The scaleset controller has been down for more than 5 minutes (metrics endpoint not reporting).

## Impact

Runner scale sets will not be created, updated, or scaled. Existing runner pods may continue until the controller is restored.

## Steps

1. Check controller manager deployment and pods: `kubectl get deployment -n system controller-manager; kubectl get pods -n system -l app.kubernetes.io/name=scaleset-controller`
2. Inspect pod logs: `kubectl logs -n system -l app.kubernetes.io/name=scaleset-controller --tail=200`
3. Check events: `kubectl get events -n system --sort-by='.lastTimestamp'`
4. Verify leader election if enabled: `kubectl get lease -n system`
5. Restart the deployment if the pod is CrashLoopBackOff or not ready: `kubectl rollout restart deployment/controller-manager -n system`
