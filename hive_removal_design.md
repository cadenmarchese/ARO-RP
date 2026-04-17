# Hive Removal Design

## Context

**Why this change:** Currently, ARO-RP relies on Hive (an OpenShift cluster lifecycle controller) to manage cluster provision pods during installation. This adds an external dependency and complexity. The goal is to eliminate Hive entirely and have ARO-RP directly create and manage cluster provision pods in the same AKS clusters where Hive currently runs.

**What Hive actually does:**
1. **During installation:** Creates and monitors provision pods that run the OpenShift installer
2. **After installation:** Maintains ClusterDeployment resource and monitors cluster reachability
3. **During deletion:** Cleaned up via `hiveDeleteResources()` which deletes the namespace

**Why complete removal is safe:**
- Hive monitoring is redundant - ARO already has comprehensive cluster health monitoring in `pkg/monitor/cluster/`
- SyncSet functionality is unused - ARO has read-only admin APIs for inspection but never creates SyncSets
- No ongoing cluster management - Hive just maintains ClusterDeployment status, which duplicates existing monitoring

**Current Architecture:**
- ARO-RP calls `hiveClusterManager.Install()` → Creates `ClusterDeployment` CRD in AKS
- Hive controllers watch `ClusterDeployment` → Automatically create provision pods
- Hive manages pod lifecycle, secrets, and monitoring
- ARO-RP polls `ClusterDeployment` status for completion

**New Architecture:**
- ARO-RP directly creates provision pods in AKS using Kubernetes clients
- ARO-RP manages pod lifecycle (create, monitor, cleanup) without Hive intermediary
- Complete removal of Hive code, imports, and dependencies

**Foundation:** The `pkg/containerinstall` package already implements similar functionality for local development using podman. This plan adapts that pattern to use Kubernetes clients for production AKS clusters.

## Implementation Approach

### 1. Create New Package: `pkg/aksinstall`

A new package that implements the same `ContainerInstaller` interface as `pkg/containerinstall`, but uses Kubernetes clients to manage pods in AKS instead of podman.

**Key Components:**

- **manager.go**: Core installer struct with Kubernetes client, namespace management
- **install.go**: Main orchestration using steps framework (namespace → secrets → pod → monitor → cleanup)
- **pod.go**: Pod specification, creation, and status monitoring
- **secrets.go**: Kubernetes secret creation for cluster config, credentials, manifests
- **failure.go**: Error parsing from installer logs (adapted from `pkg/hive/failure/handler.go`)
- **logs.go**: Pod log retrieval for debugging

**Manager Structure:**
```go
type manager struct {
    log           *logrus.Entry
    env           env.Interface
    m             metrics.Emitter        // For emitting metrics
    restConfig    *rest.Config           // AKS cluster connection
    kubernetescli kubernetes.Interface   // K8s client for pod/secret operations
    clusterUUID   string                 // From doc.ID
    namespace     string                 // "aro-{docID}"
    dims          map[string]string      // Metric dimensions (resource ID, etc.)
    success       bool                   // Installation success flag
}
```

**Installation Flow (using steps framework):**
1. `ensureNamespace()` - Create `aro-{docID}` namespace in AKS
2. `createSecrets()` - Create K8s secrets for cluster doc, subscription, certs, manifests
3. `createPod()` - Create provision pod with installer image
4. `podFinished()` - Poll pod status (60-minute timeout)
5. `cleanup()` - Delete pod and secrets (keep namespace on failure for debugging)

### 2. Pod Specification

The provision pod will run the OpenShift installer with mounted secrets and tmpfs volumes:

**Container Image:** `version.Properties.InstallerPullspec`  
**Entrypoint:** `/bin/bash -c "/bin/openshift-install create manifests && /bin/openshift-install create cluster"`

**Volumes:**
- `azure-credentials-{uuid}` - Secret with cluster doc (`99_aro.json`), subscription (`99_sub.json`), proxy certs
- `custom-manifests-{uuid}` - Secret with custom YAML manifests (workload identity, disabled samples)
- `bound-sa-signing-key-{uuid}` - Secret for workload identity clusters
- `tmpfs-azure`, `tmpfs-manifests`, `tmpfs-output` - EmptyDir (memory) for installer working directories

**Volume Mounts:**
- `/.azure` - Azure credentials and cluster config
- `/manifests` - Custom manifests
- `/boundsasigningkey` - Service account signing key
- `/output` - Installer output

**Environment Variables:**
- `ARO_UUID={doc.ID}`
- `OPENSHIFT_INSTALL_INVOKER=aro`
- `OPENSHIFT_INSTALL_RELEASE_IMAGE_OVERRIDE={version.Properties.OpenShiftPullspec}`
- Development mode: `ARO_RP_MODE=development` + dev env vars

**Pod Labels:**
- `aro-cluster-id: {uuid}` - For identification
- `aro-installer: true` - For filtering

### 3. AKS Client Acquisition

Reuse existing pattern from `pkg/util/liveconfig/hive.go`:

```go
// In aksinstall.New():
hiveShard := 1  // Same shard logic as Hive
restConfig, err := liveConfig.HiveRestConfig(ctx, hiveShard)
kubernetescli, err := kubernetes.NewForConfig(restConfig)
```

**AKS Cluster Connection:**
- Uses existing `getAksShardKubeconfig()` function
- Connects to `aro-aks-cluster-{shard:03d}` (same clusters where Hive runs)
- Retrieves admin credentials via `ListClusterAdminCredentials()`
- Caches credentials in liveconfig with RWMutex

### 4. Monitoring and Error Handling

**Status Monitoring (`podFinished()`):**
- Poll `pod.Status.Phase` every 10 seconds
- Timeout after 60 minutes (same as Hive)
- Handle pod phases:
  - `PodSucceeded` → Set `success=true`, return
  - `PodFailed` → Retrieve logs, parse errors, return error
  - `PodPending`/`PodRunning` → Continue polling

**Log Retrieval:**
```go
req := kubernetescli.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{})
stream := req.Stream(ctx)
// Read logs into buffer for error analysis
```

**Error Parsing:**
Adapt `pkg/hive/failure/handler.go` patterns to parse installer logs and map to Azure-specific errors:
- `AzureRequestDisallowedByPolicy`
- `AzureInvalidTemplateDeployment`
- `AzureZonalAllocationFailed`
- `AzureOSProvisioningTimedOut`
- `AzureKeyBasedAuthenticationNotPermitted`

Return structured CloudError for user-facing error messages.

**Cleanup Strategy:**
- **On success:** Delete pod, delete all secrets, delete namespace
- **On failure:** Delete pod, delete secrets, **keep namespace** for 24h debugging
- Background garbage collection job for stale namespaces (future enhancement)

### 5. Integration with Cluster Install Flow

**File:** `pkg/cluster/install.go`

**Add new method** (similar to `runPodmanInstaller` at line 322-347):
```go
func (m *manager) runAKSInstaller(ctx context.Context) error {
    version, err := m.openShiftVersionFromVersion(ctx)
    if err != nil {
        return err
    }
    
    customManifests := map[string]kruntime.Object{}
    if m.doc.OpenShiftCluster.UsesWorkloadIdentity() {
        workloadIdentityManifests, err := m.generateWorkloadIdentityResources()
        if err != nil {
            return err
        }
        maps.Copy(customManifests, workloadIdentityManifests)
    }
    
    if m.shouldDisableSamples() {
        customManifests["cluster-config-samples.yaml"] = bootstrapDisabledSamplesConfig()
    }
    
    i, err := aksinstall.New(ctx, m.log, m.env, m.doc.ID, m.liveConfig)
    if err != nil {
        return err
    }
    
    return i.Install(ctx, m.subscriptionDoc, m.doc, version, customManifests)
}
```

**Modify `bootstrap()` method** (around line 440-470):
- **Remove:** `hiveCreateNamespace`, `runHiveInstaller`, `hiveClusterInstallationComplete` steps
- **Remove:** `hiveEnsureResources`, `hiveClusterDeploymentReady` steps (adoption flow)
- **Replace with:** Direct call to `runAKSInstaller` with same pattern as `runPodmanInstaller`

**Before:**
```go
if m.installViaHive {
    s = append(s,
        steps.Action(m.hiveCreateNamespace),
        steps.Action(m.runHiveInstaller),
        steps.Condition(m.hiveClusterInstallationComplete, 60*time.Minute, true),
        steps.Action(m.generateKubeconfigs),
    )
}
```

**After:**
```go
// All cluster installations now use AKS direct
s = append(s,
    steps.Action(m.runAKSInstaller),
    steps.Action(m.generateKubeconfigs),
)
```

### 6. Complete Hive Removal

**Delete entire packages:**
- `pkg/hive/` - All Hive integration code
- `pkg/monitor/hive/` - Hive-based monitoring

**Remove from cluster manager:**
- `pkg/cluster/cluster.go` - Remove `hiveClusterManager` field
- `pkg/cluster/hive.go` - Delete file (Hive wrapper methods)
- `pkg/cluster/install.go` - Remove `runHiveInstaller()`, `hiveCreateNamespace()`, `hiveClusterInstallationComplete()`, `hiveEnsureResources()`, `hiveClusterDeploymentReady()`, `hiveResetCorrelationData()`

**Remove from backend:**
- `pkg/backend/openshiftcluster.go` - Remove Hive client creation logic (lines 113-134)

**Remove from monitor:**
- `pkg/monitor/monitor.go` - Remove `hiveMonitorBuilder` initialization
- Remove Hive monitor from monitor chain

**Remove from liveconfig:**
- `pkg/util/liveconfig/hive.go` - Remove `InstallViaHive()`, `AdoptByHive()` methods
- Keep `HiveRestConfig()` but rename to `AKSRestConfig()` for clarity

**Remove from frontend:**
- `pkg/frontend/admin_hive_syncset_list.go` - Delete file
- `pkg/frontend/admin_hive_syncset_get.go` - Delete file
- `pkg/frontend/admin_hive_syncset_resources.go` - Delete file (if exists)
- `pkg/frontend/frontend.go` - Remove SyncSet admin API route registrations
- `pkg/frontend/frontend.go` - Remove `hiveSyncSetManager` field

**Remove imports:**
- Search and remove all imports of `github.com/openshift/hive/apis/hive/v1`
- Search and remove all imports of `github.com/openshift/hive/apis/hive/v1alpha1`
- Remove from `go.mod` dependencies (both root and `pkg/api/go.mod`)

**Remove environment variables:**
- Remove `HIVE_INSTALLER_ENABLE` env var checks
- Remove `HIVE_ADOPT_ENABLE` env var checks
- Remove `HIVE_DEFAULT_PULLSPEC` env var

**Update API types:**
- `pkg/api/openshiftcluster.go` - Remove `HiveProfile.CreatedByHive` field (or mark deprecated)

### 7. Monitoring Removal

**Current Hive Monitoring (pkg/monitor/hive/):**
1. `emitHiveRegistrationStatus()` - Checks ClusterDeployment conditions:
   - `ClusterReadyCondition` should be True
   - `UnreachableCondition` should be False
   - Emits `hive.clusterdeployment.conditions` metrics

2. `emitClusterSync()` - Monitors SyncSet/SelectorSyncSet status:
   - Tracks success/failure of SyncSet applications
   - Emits `hive.clustersync` metrics
   - Note: ARO doesn't create SyncSets, only has read-only admin APIs to inspect them

**What Hive Does After Installation:**
- Maintains ClusterDeployment resource with cluster state
- Continuously reconciles cluster state (checks if cluster is reachable)
- Provides health signals via ClusterDeployment conditions
- Deleted via `hiveDeleteResources()` when cluster is deleted

**Removal Strategy:**
Complete removal of Hive monitoring. The metrics tracked by Hive monitoring are:
- Redundant: ARO already monitors cluster health directly via kubeconfig (ClusterOperator status, node health, etc.)
- Hive-specific: These metrics only make sense when using Hive for installation/management
- Low value: ARO doesn't use SyncSets, so SyncSet monitoring provides no value

Since ARO will no longer use Hive at all, these Hive-specific metrics become obsolete. Cluster health is already monitored through other mechanisms in `pkg/monitor/cluster/`.

**Important:** The Hive monitoring (`pkg/monitor/hive/`) runs periodically on ALL clusters via the monitor loop. This is separate from installation-time monitoring. Installation failures are tracked differently:
- Backend sets cluster ProvisioningState to Failed when installation fails
- Async operation status is updated with error details
- Cluster document stores failure reason

For provision pod failures during installation, we need new metrics emitted by `pkg/aksinstall` (see section 8 below).

### 8. Installation Metrics and Observability

The `pkg/aksinstall` package should emit metrics during installation to replace the observability previously provided by Hive monitoring:

**Metrics to Emit (in pkg/aksinstall/install.go):**
```go
// At start of installation
m.emitGauge("aksinstall.installation.started", 1, dims)

// On successful completion
m.emitGauge("aksinstall.installation.succeeded", 1, dims)
m.emitFloat("aksinstall.installation.duration", duration.Seconds(), dims)

// On failure
m.emitGauge("aksinstall.installation.failed", 1, dims)
m.emitGauge("aksinstall.installation.failed.reason", 1, map[string]string{
    "error_type": parsedErrorType, // e.g., "AzureRequestDisallowedByPolicy"
})

// Pod lifecycle metrics
m.emitGauge("aksinstall.pod.created", 1, dims)
m.emitGauge("aksinstall.pod.phase", 1, map[string]string{"phase": podPhase})
```

**Logging:**
- Log provision pod creation with correlation IDs
- Log pod phase transitions (Pending → Running → Succeeded/Failed)
- Log installer output on failure for debugging
- Preserve correlation data (request IDs, client principal) in pod labels/annotations

**Error Handling:**
- Parse installer logs using adapted `pkg/hive/failure/handler.go` patterns
- Map to user-friendly CloudError messages
- Include pod logs in error context for support debugging

This ensures we have equal or better observability compared to Hive monitoring, but during installation rather than as periodic monitoring.

## Critical Files

### Files to Create:
1. `pkg/aksinstall/manager.go` - Core installer interface and struct
2. `pkg/aksinstall/install.go` - Installation orchestration with metrics emission
3. `pkg/aksinstall/pod.go` - Pod specification and lifecycle
4. `pkg/aksinstall/secrets.go` - Kubernetes secret management
5. `pkg/aksinstall/failure.go` - Error handling and log parsing (adapted from pkg/hive/failure)
6. `pkg/aksinstall/logs.go` - Pod log retrieval
7. `pkg/aksinstall/metrics.go` - Metrics helper methods (emitGauge, emitFloat)

### Files to Modify:
7. `pkg/cluster/install.go` - Add `runAKSInstaller()`, modify `bootstrap()` to remove Hive paths
8. `pkg/cluster/cluster.go` - Remove `hiveClusterManager` field, add `liveConfig` if not present
9. `pkg/backend/openshiftcluster.go` - Remove Hive client creation
10. `pkg/monitor/monitor.go` - Remove Hive monitor builder
11. `pkg/frontend/frontend.go` - Remove SyncSet admin API routes and hiveSyncSetManager field
12. `pkg/util/liveconfig/hive.go` - Remove Hive feature flags, optionally rename `HiveRestConfig`
13. `go.mod` - Remove Hive dependencies
14. `pkg/api/go.mod` - Remove Hive dependencies if present

### Files to Delete:
15. `pkg/hive/*.go` - All files in hive package
16. `pkg/monitor/hive/*.go` - All files in monitor/hive package
17. `pkg/cluster/hive.go` - Hive wrapper methods
18. `pkg/frontend/admin_hive_syncset_list.go` - Admin SyncSet list API
19. `pkg/frontend/admin_hive_syncset_get.go` - Admin SyncSet get API
20. `pkg/frontend/admin_hive_syncset_resources.go` - Admin SyncSet resources (if exists)
21. `pkg/frontend/admin_hive_syncset_list_test.go` - Test file
22. `pkg/frontend/admin_hive_syncset_get_test.go` - Test file
23. `pkg/frontend/admin_hive_syncset_resources_test.go` - Test file (if exists)

## Verification

After implementation, verify the changes work correctly:

1. **Local Development Test:**
   - Set up local AKS cluster or use existing dev environment
   - Create new ARO cluster installation
   - Verify provision pod is created in AKS `aro-{docID}` namespace
   - Verify secrets are created correctly
   - Monitor pod logs to ensure installer runs
   - Verify cleanup happens on success

2. **Integration Test:**
   - Run full cluster installation in dev/int environment
   - Verify cluster installs successfully without Hive
   - Check that no Hive resources are created
   - Verify kubeconfigs are generated correctly
   - Test workload identity clusters (bound SA signing key mounting)

3. **Error Handling Test:**
   - Trigger installation failures (quota, policy violations)
   - Verify pod logs are captured
   - Verify error parsing produces user-friendly messages
   - Verify namespace is kept on failure for debugging

4. **Cleanup Test:**
   - Successful install → Verify namespace/pod/secrets deleted
   - Failed install → Verify namespace kept, pod/secrets deleted
   - Manual cleanup of stale namespaces

5. **Unit Tests:**
   - Test pod spec generation
   - Test secret creation
   - Test error parsing logic
   - Test status monitoring state machine

6. **Build Verification:**
   - `make fmt` - Format both modules
   - `make unit-test-go` - Root module tests
   - `cd pkg/api && go test ./...` - API module tests
   - `make lint-go` - No new lint violations
   - `make go-tidy` - Clean dependencies

## Risks and Mitigations

**Risk 1: Breaking Existing Clusters**
- Impact: Existing clusters that were installed via Hive will lose Hive monitoring
- Analysis: Hive monitoring (`pkg/monitor/hive/`) checks ClusterDeployment health, but:
  - ARO already has comprehensive cluster monitoring via `pkg/monitor/cluster/` 
  - Hive monitoring is redundant - checks cluster reachability/readiness which is already monitored
  - SyncSet monitoring provides no value (ARO doesn't create SyncSets)
  - When clusters are deleted, `hiveDeleteResources()` cleans up Hive namespace
- Mitigation: Existing clusters will lose Hive-specific metrics but retain all functional monitoring. No operational impact.

**Risk 2: Admin SyncSet APIs Break**
- Impact: Admin APIs for listing/getting SyncSets will fail after Hive removal
- Analysis: These are read-only diagnostic APIs at `/admin/hivesyncsets`. Removing them with Hive.
- Mitigation: Remove admin API endpoints along with Hive code. Document as removed functionality.

**Risk 3: Direct Replacement (No Gradual Rollout)**
- Impact: All new clusters immediately use new code path, higher risk if bugs exist
- Mitigation: Thorough testing in dev/int, fast rollback capability, comprehensive error handling

## Out of Scope

The following are explicitly out of scope for this implementation:

1. **Multi-shard AKS support** - Currently hardcoded to shard 1, can be enhanced later
2. **Garbage collection automation** - Background job to clean stale namespaces (manual cleanup initially)
3. **Existing cluster migration** - Existing clusters keep their HiveProfile in the document but Hive monitoring is removed
4. **Monitoring replacement** - Not needed; cluster health already monitored via pkg/monitor/cluster/

## Definition of Done

- [ ] `pkg/aksinstall` package created with full implementation
- [ ] `pkg/cluster/install.go` modified to use `runAKSInstaller()`
- [ ] All Hive code removed: `pkg/hive/`, `pkg/monitor/hive/`, `pkg/cluster/hive.go`
- [ ] Hive imports removed from all files
- [ ] Hive dependencies removed from `go.mod` files
- [ ] `make fmt` passes (both modules)
- [ ] `make unit-test-go` passes
- [ ] `cd pkg/api && go test ./...` passes
- [ ] `make lint-go` passes with no new violations
- [ ] Integration test: Successful cluster installation without Hive
- [ ] Error handling test: Failed installation produces correct error messages
- [ ] Cleanup test: Namespace deleted on success, kept on failure
- [ ] No runtime errors or panics during installation
