# RSSC (RunnerSet Scale Controller)

RSSC is a Kubernetes operator that manages GitHub Actions self-hosted runner scale sets. It dynamically creates and deletes runner pods based on job queue demand.

- **API group**: `scaleset.actions.github.com`
- **API version**: `v1alpha1`
- **Module**: `github.com/coolguy1771/rssc`
- **Go**: 1.25.3

## Features

- **AutoscaleSet**: Register and manage a GitHub Actions scale set (GitHub URL, credentials, runner image, min/max runners, labels). The controller creates/updates/deletes the scale set via GitHub's API and manages a finalizer for cleanup on deletion.
- **RunnerSet**: Reference an AutoscaleSet and runner group; open a message session with GitHub, run listeners, and scale runner pods. Pod spec changes (via SHA256 hash) trigger idle pod deletion and listener restart.
- **Scaling**: Pod creation/deletion driven by job queue demand; batch operations and runner state (idle/busy) tracking.
- **Observability**: Prometheus metrics for reconciliation, scaling, runners, jobs, sessions, listeners, API calls, and cache rates; optional Grafana dashboards.

## Prerequisites

- Go 1.25.3+
- Kubernetes cluster and `kubeconfig` for deployment
- GitHub App (client ID, installation ID, private key secret) or Personal Access Token for GitHub API

## Installation

1. Install CRDs:

   ```bash
   make install
   ```

2. Create a Secret with GitHub credentials (GitHub App or PAT). For GitHub App, the secret must contain the private key; the controller expects the key under the key referenced in `AutoscaleSet.spec.githubApp.privateKeySecretRef`.

3. Deploy the controller (default kustomize stack):

   ```bash
   make deploy IMG=your-registry/scaleset-controller:tag
   ```

4. Apply sample resources from `config/samples/` (edit `autoscaleset-sample` to point at your GitHub org and secret).

## Custom Resources

### AutoscaleSet

Defines a GitHub Actions scale set: `githubConfigURL`, `scaleSetName`, `runnerImage`, `runnerGroup`, `minRunners`, `maxRunners`, labels, and an optional pod template (`runnerTemplate`). Authentication is via `spec.githubApp` (clientID, installationID, privateKeySecretRef) or a token secret.

Example: `config/samples/scaleset_v1alpha1_autoscaleset.yaml`

### RunnerSet

References an AutoscaleSet by `spec.autoscaleSetRef` (name and namespace) and a `runnerGroup`. It can override runner pod metadata/spec via `runnerTemplate`. The controller opens a message session, starts listeners per runner group, and scales pods based on demand.

Example: `config/samples/scaleset_v1alpha1_runnerset.yaml`

## Build and run

```bash
# Generate CRDs, RBAC, webhooks and deepcopy code
make generate manifests

# Build manager binary
make build

# Run locally (needs kubeconfig)
make run
```

Entry point is `cmd/main.go` (webhooks, leader election, metrics, listener shutdown, cache cleanup).

## Testing

```bash
# Unit/controller tests (Ginkgo + envtest)
make test

# Single test
go test -v -run TestSpecificName ./internal/controller/...

# E2E (Kind cluster; creates cluster if missing)
make test-e2e
```

## Deployment options

- **Kustomize**: `config/default`; set controller image with `make deploy IMG=...`.
- **Single installer YAML**: `make build-installer` produces `dist/install.yaml`.
- **OLM**: `make bundle`, `make bundle-build` (see Makefile for `BUNDLE_IMG`, `VERSION`, channels).

## Observability

- Metrics are exposed by the controller; import `internal/metrics` (e.g. blank import in `cmd/main.go`). Prometheus rules live under `config/prometheus/`.
- Grafana dashboards: `grafana/` (controller-runtime and custom metrics).

## Configuration layout

- **CRDs**: `config/crd/bases/`
- **Manager/RBAC/Webhook**: `config/manager/`, `config/rbac/`, `config/webhook/`
- **Samples**: `config/samples/`
- **OLM**: `config/manifests/`, `config/scorecard/`

## Runner pod resources

In namespaces with ResourceQuota or LimitRange, set `runnerTemplate.spec.resources.requests` (and optionally `limits`) on the AutoscaleSet or RunnerSet so runner pods are admitted. The controller does not apply default resource requests.

## Conventions

- **Finalizers**: `scaleset.actions.github.com/finalizer` (AutoscaleSet), `scaleset.actions.github.com/runnerset-finalizer` (RunnerSet)
- **Labels**: `scaleset.actions.github.com/runner-name`, `runner-state`, `autoscaleset`, `runner-group`
- **Annotations**: `scaleset.actions.github.com/jit-config`, `pod-spec-hash`, `last-autoscaleset-generation`

Credentials must not be hardcoded; use Kubernetes Secrets and reference them from the AutoscaleSet spec.
