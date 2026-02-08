# ScaleSetCreationFailed

## Summary

Scale set creation operations are failing (create operations returning error in metrics).

## Impact

AutoscaleSet resources will not get a ScaleSetID; RunnerSets depending on them will wait or report error.

## Steps

1. List AutoscaleSets with no or stale status: `kubectl get autoscalesets -A -o custom-columns='NS:.metadata.namespace,NAME:.metadata.name,SCALE_SET_ID:.status.scaleSetID'`
2. Describe failing resource: `kubectl describe autoscaleset -n <ns> <name>` and check status conditions and events
3. Verify GitHub credentials Secret exists and has correct keys (e.g. private key for GitHub App, or token for PAT)
4. Check controller logs for this resource: `kubectl logs -n system -l app.kubernetes.io/name=scaleset-controller --tail=300` and search for the AutoscaleSet name
5. Confirm GitHub API accessibility and that scale set name is unique within the runner group
