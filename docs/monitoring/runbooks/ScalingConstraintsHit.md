# ScalingConstraintsHit

## Summary

Scaling operations are being constrained by min/max runner limits (rate > 0.1/s over 10m).

## Impact

Informational: the controller is at min or max runner count and cannot scale down or up further. May correlate with HighJobQueueTime if at max.

## Steps

1. Review AutoscaleSet minRunners and maxRunners: `kubectl get autoscalesets -A -o yaml | grep -A2 minRunners`
2. If jobs are queuing, consider raising maxRunners or adding capacity (more RunnerSets / groups)
3. If at min and runners are idle, this is expected; no action required unless you want to reduce minRunners to save resources
