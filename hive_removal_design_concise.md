# Hive Removal - Implementation Overview

## Current State

Today, ARO-RP uses Hive (an external OpenShift controller) as an intermediary to manage cluster installations:
- ARO-RP creates a `ClusterDeployment` custom resource in AKS
- Hive watches this resource and creates/manages the installer pod
- ARO-RP polls Hive for installation status

## Proposed Solution

Rather than Hive, ARO-RP itself will create and manage OpenShift installer pods directly in AKS. We already have a working implementation for local development (`pkg/containerinstall`) that runs the installer in podman. This proposal adapts that same pattern to use Kubernetes clients for production AKS clusters.

## Implementation Overview

### 1. New ARO-RP Package: `pkg/aksinstall`

We will create installer pods directly in AKS using the Kubernetes API using the same `ContainerInstaller` interface as the existing podman-based installer. There will be shared logic between this new package and the existing `pkg/containerinstall`, but with different clients. Re-use of the existing `pkg/containerinstall` package with environment-specific configuration is also possible, if preferred.

**Installation flow** (same steps used today in Hive):
```
1. Create namespace (aro-{clusterID})
2. Create secrets (cluster config, credentials, manifests)
3. Create installer pod
4. Monitor pod until completion (60min timeout)
5. Cleanup (delete pod/secrets, keep namespace on failure for debugging)
```

### 2. Infrastructure Reuse

- We will re-use the same AKS clusters where Hive currently runs (`aro-aks-cluster-{shard}`) as well as AKS client code from `pkg/util/liveconfig/hive.go`
- We will continue the same namespace pattern, same credential retrieval, and same admin access

At first, the installer pods will be neighbors to the current Hive pods, then Hive will be removed.

### 3. Pod Specification

There are no changes required here. The installer pod runs the standard OpenShift installer with mounted configuration. It uses an OpenShift installer image from version catalog and runs `openshift-install create manifests && openshift-install create cluster`.

**Installer Pod Contents**:
- Azure credentials and cluster config from Kubernetes secrets
- Custom manifests (workload identity, disabled samples)
- Temporary volumes for installer working directories

**Installer Pod Labels** for identification and monitoring:
- `aro-cluster-id: {uuid}`
- `aro-installer: true`

### 4. Operability and Observability

There will be a small amount of work required to transfer over our exiting Hive observability to the new ARO-RP flow:

**Pod monitoring** (`pkg/aksinstall/pod.go`):
- Implement `podFinished()` using pattern from `pkg/containerinstall/install.go:170-186` (containerFinished)
- Replace podman API (`containers.Inspect`) with K8s API (`pods.Get().Status.Phase`)
- On failure, replace `getContainerLogs()` with `kubernetescli.CoreV1().Pods().GetLogs()`
- Keep 60-minute timeout from existing implementation

**Error parsing / regex changes** (`pkg/aksinstall/failure.go`):
- Copy regex patterns and error mapping from `pkg/hive/failure/handler.go`
- Remove Hive log unwrapping logic (installer output is direct, not nested in Hive's JSON)
- Keep CloudError type mappings: `AzureRequestDisallowedByPolicy`, `AzureZonalAllocationFailed`, etc.

**New Metrics Required** (`pkg/aksinstall/install.go`):
- Add metrics emission similar to Hive's current metrics but with `aksinstall.*` prefix
- Installation lifecycle: `started`, `succeeded`, `failed`, `duration`, `failed.reason`

**Alerting and IcM automation changes**:
- Update namespace filters: `namespace=~"hive.*"` → `namespace=~"aro-.*"`
- Rename alert titles for clarity
- Simplify log queries: remove Hive controller log parsing, read pod logs directly
- Remove ClusterDeployment CRD queries
- Update correlation data source: pod labels instead of ClusterDeployment annotations

### 5. Rollout Strategy

**Environment-based rollout using RP_FEATURES / RP-Config**:

The changes can be fully rolled out before we go live using RP-Config / RP features. We will keep Hive intact during the rollout in case there are issues. We can easily disable the RP feature flag to revert to Hive path if issues arise.

```go
if m.env.DeploymentMode() == development || m.env.FeatureIsSet(FeatureUseAKSInstaller) {
    // New path: direct AKS installer
    s = append(s,
        steps.Action(m.runAKSInstaller),
        steps.Action(m.generateKubeconfigs),
    )
} else if m.installViaHive {
    // Legacy path (to be removed after validation)
    s = append(s, hive installation steps...)
}
```

**Phase 1: Development and Testing**
- Implement `pkg/aksinstall` package
- Enable feature flag in local development
- Validate successful installations in dev environment

**Phase 2: Staging Environment**
- Enable feature flag in STG
- Run integration tests (successful install, workload identity clusters, error handling)
- Validate metrics and alerting

**Phase 3: Canary Rollout**
- Enable in canary region(s)
- Monitor installation success rate
- Validate incident automation with real failures

**Phase 4: Production and Cleanup**
- Enable in all production regions
- Monitor for issues
- Once stable, proceed to Hive cleanup

### 6. Hive Cleanup Scope

- Remove `pkg/hive/` integration code
- Remove `pkg/monitor/hive/` monitoring (redundant)
- Remove Admin APIs for Hive SyncSets (unused read-only endpoints)
- Cluster manager: Remove `hiveClusterManager` field and wrapper methods
- Backend: Remove Hive client creation
- Monitor: Remove Hive monitor from monitoring chain
- Frontend: Remove SyncSet admin API routes
- Remove Hive imports from all files
- Remove Hive dependencies from `go.mod` (both modules)
- Remove Hive-related environment variables

**Why complete removal is safe**:
- Hive monitoring is redundant - ARO already monitors cluster health directly
- SyncSet functionality is unused - ARO has read-only APIs but never creates SyncSets
- No ongoing cluster management - Hive just maintains status that duplicates existing monitoring