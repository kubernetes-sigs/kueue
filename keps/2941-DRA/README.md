# KEP-2941: DRA Support in Kueue

<!--
This is the title of your KEP. Keep it short, simple, and descriptive. A good
title can help communicate what the KEP is and should be considered as part of
any review.
-->

<!--
A table of contents is helpful for quickly jumping to sections of a KEP and for
highlighting any additional information provided beyond the standard KEP
template.

Ensure the TOC is wrapped with
  <code>&lt;!-- toc --&rt;&lt;!-- /toc --&rt;</code>
tags, and then generate with `hack/update-toc.sh`.
-->

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Background](#background)
    - [DRA Example](#dra-example)
    - [Workload Example](#workload-example)
    - [Example Driver Cluster Resources](#example-driver-cluster-resources)
      - [ResourceSlices](#resourceslices)
      - [DeviceClasses](#deviceclasses)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
    - [Story 4](#story-4)
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Configuration API Extension for DRA](#configuration-api-extension-for-dra)
  - [Device Class Resolution and Conflict Prevention](#device-class-resolution-and-conflict-prevention)
    - [Device Class Mapping Uniqueness](#device-class-mapping-uniqueness)
  - [RBAC Requirements](#rbac-requirements)
  - [Workloads](#workloads)
    - [DRA-Specific Workload Processing](#dra-specific-workload-processing)
    - [Workload Processing Flow](#workload-processing-flow)
  - [Extended Resources](#extended-resources)
    - [Configuration](#configuration)
    - [Path Separation](#path-separation)
    - [Processing Flow](#processing-flow)
    - [Same Hardware with Both Paths](#same-hardware-with-both-paths)
    - [DeviceClass Resolution via Field Indexer](#deviceclass-resolution-via-field-indexer)
    - [DeviceClass Lifecycle Scenarios](#deviceclass-lifecycle-scenarios)
    - [Late DeviceClass Creation](#late-deviceclass-creation)
  - [Architecture Details](#architecture-details)
    - [Queue Manager Extensions](#queue-manager-extensions)
  - [MultiKueue Integration](#multikueue-integration)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [E2E Test](#e2e-test)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Webhook Rewriting Extended Resources to ResourceClaimTemplates](#webhook-rewriting-extended-resources-to-resourceclaimtemplates)
  - [ResourceClaim By Count](#resourceclaim-by-count)
  - [Using devices in ResourceSlice to Count](#using-devices-in-resourceslice-to-count)
  - [Using a CEL expression](#using-a-cel-expression)
  - [Defining DeviceClass mapping in ClusterQuota](#defining-deviceclass-mapping-in-clusterquota)
  - [Using ResourceFlavor for DeviceClass Mapping](#using-resourceflavor-for-deviceclass-mapping)
  - [Creating a new CRD for device class mapping](#creating-a-new-crd-for-device-class-mapping)
<!-- /toc -->

## Summary

[Dynamic Resource Allocation (DRA)](https://kubernetes.io/docs/concepts/scheduling-eviction/dynamic-resource-allocation/)
is a major effort to improve device support in Kubernetes. It changes how one can request resources in a myriad of ways.

This KEP supports two approaches for DRA integration with Kueue:
1. **ResourceClaimTemplates**: Pods explicitly reference ResourceClaimTemplates that specify device requests.
2. **Extended Resources**: Pods request DRA devices via standard `resources.requests` (e.g., `example.com/gpu: 1`), and kube-scheduler automatically creates ResourceClaims when the DeviceClass has an `extendedResourceName` field set.

## Motivation

Dynamic Resource Allocation (DRA) provides the groundwork for more sophisticated device allocations to Pods.
Quota management is about enforcing rules around the use of resources.
For example, GPUs are resource constrained and a popular request is the ability to enforce fair sharing of GPU
resources.
With these devices, many users want access and sometimes some users want the ability to preempt other users if their
workloads have a higher priority. Kueue provides support for this.

DRA provides a future where users could schedule partitionable GPU devices (MIG) or time slicing. As devices gain a
more robust way to schedule, it is important to walk through how support of DRA will work with Kueue.

### Background

DRA has four APIs that are relevant for a Kueue:

- ResourceClaims
- ResourceClaimTemplates
- DeviceClasses
- ResourceSlices

#### DRA Example

The easiest way to test DRA is to
use [dra example driver repository](https://github.com/kubernetes-sigs/dra-example-driver). Cloning that repo and
running
`make setup-e2e` will create a Kind cluster with the DRA feature gate and install a mock dra driver. This does not use
actual GPUs so it is perfect for a test environment for exploring Kueue and DRA integration.

#### Workload Example

An example workload that uses DRA:

```yaml
---

apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  namespace: gpu-test1
  name: single-gpu
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly:
          deviceClassName: gpu.example.com
---
apiVersion: batch/v1
kind: Job
metadata:
  namespace: gpu-test1
  name: job0
  labels:
    app: job
    kueue.x-k8s.io/queue-name: user-queue
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: ctr0
        image: ubuntu:22.04
        command: ["bash", "-c"]
        args: ["export; sleep 9999"]
        resources:
          claims:
          - name: gpu
          requests:
            cpu: 1
            memory: "200Mi"
      resourceClaims:
      - name: gpu
        resourceClaimTemplateName: single-gpu
```

#### Example Driver Cluster Resources

The dra-example-driver creates a ResourceSlice for each node and a DeviceClass named `gpu.example.com` for the entire
cluster.

##### ResourceSlices

ResourceSlices are meant for communication between drivers and the control planes. These are not expected to be used for
workloads.

##### DeviceClasses

Each driver creates a device class and every resource claim will reference the device class. The dra-example-driver has
a simple device class named `gpu.example.com`. This will be the way to enforce quota limits.

### Goals

- Users can submit workloads using ResourceClaimTemplates and Kueue can monitor the usage.
- Users can submit workloads using extended resource requests (e.g., `example.com/gpu: 1`) and
  Kueue can account for quota when the DeviceClass has `extendedResourceName` set.
- Admins can enforce the quota for number of devices for a given DeviceClass.

### Non-Goals

- Quota-aware handling of DRAAdminAccess and DRAPrioritizedLists (beta, default enabled in K8s 1.35)
  is not included in Kueue's alpha. See [Risks and Mitigations](#risks-and-mitigations) for the
  planned approach.
- Support for alpha DRA features like DRADeviceTaints and DRAPartitionableDevices will not be included.
- This design does not work with Topology Aware Scheduling feature of Kueue. It is a significant
  amount of work, will be addressed in the future with a separate body of work.

## Proposal

This proposal extends Kueue to support workloads using DRA APIs for quota management, borrowing and preemptable
scheduling. This includes:

1. Extending the existing Kueue Configuration API with `DeviceClassMappings` to map device classes to logical resource
   names
2. Supporting workloads that use ResourceClaimTemplates (ResourceClaims are not supported in alpha)
3. Supporting workloads that use extended resource requests backed by DRA DeviceClasses with
   `extendedResourceName` set (requires Kubernetes `DRAExtendedResource` feature gate, alpha in k8s 1.35)
4. Allowing admins to define quota for DRA resources in ClusterQueues using the logical resource names from device class
   mappings
5. Implementing validation to prevent device class conflicts and ensure predictable quota behavior

More details are documented in [Design Details](#design-details)

### User Stories (Optional)

#### Story 1

As a Kueue user, I want to use DRA devices for batch workloads in Kubernetes using Kueue

#### Story 2

As an administrator of Kueue with ClusterQueue, I have a DRA driver installed in the cluster. I would like to enforce
queuing, quota management and preemptable workloads for cluster users.

#### Story 3

As a cluster administrator, I want clear validation feedback when I misconfigure device class mappings so I can quickly
identify and fix configuration conflicts before they affect workload scheduling.

#### Story 4

As a Kueue user, I want to request DRA devices using standard resource requests (e.g., `resources.requests: {"example.com/gpu": 1}`)
instead of ResourceClaimTemplates, so my existing workloads can benefit from DRA without modification when the cluster
administrator configures DeviceClasses with `extendedResourceName`.

### Notes/Constraints/Caveats (Optional)

- The `ResourceClaims` and `ResourceClaimTemplates` APIs for DRA in k8s are immutable.
- ResourceClaims are not supported in alpha - workloads must use ResourceClaimTemplates.
  Direct ResourceClaim references will result in inadmissible workloads.
- Device class uniqueness is enforced - each device class can only map to one resource name to prevent quota ambiguity.
- Configuration-based approach - device class mappings are configured through the Kueue Configuration API
- This design does not work with Kueue's Topology Aware Scheduling feature and will be addressed in future work.
- Quota-aware handling of DRAAdminAccess and DRAPrioritizedLists (beta in K8s 1.35) is deferred
  to beta graduation. Alpha DRA features like DRADeviceTaints and DRAPartitionableDevices are
  not supported.
- **Extended Resources** requires DeviceClasses to have `spec.extendedResourceName` set.
  This depends on the Kubernetes `DRAExtendedResource` feature gate (alpha in k8s 1.35).
  When enabled, kube-scheduler automatically creates ResourceClaims for pods requesting extended resources.
  Extended resources support in Kueue is gated behind the `DRAExtendedResources` feature gate.
- **GPU time-slicing and MPS via extended resources are not supported in Alpha.**
  Time-slicing and MPS sharing modes require opaque parameters on the DeviceClass
  (e.g., `GpuConfig` with `sharing.strategy: TimeSlicing`). When kube-scheduler creates
  ResourceClaims from extended resource requests, correct quota accounting for shared
  devices requires [consumable capacity](https://github.com/kubernetes/enhancements/issues/5075)
  integration with both the DRA driver and Kueue. MPS additionally requires
  [KEP-5691 (Restricted Sharing)](https://github.com/kubernetes/enhancements/issues/5691)
  to restrict sharing to the same namespace. These will be evaluated for Beta once the
  upstream dependencies are available.
  With structured parameters, GPU sharing is supported via ResourceClaimTemplates where
  containers within the same pod share a GPU. Cross-pod sharing via direct ResourceClaims
  is not supported.

- **Kueue does not validate DeviceClass existence at config load time.** Admins should
  create DeviceClasses before submitting workloads but strict ordering is not enforced.

- **When a DeviceClass is referenced by both `deviceClassMappings` and has an
  `extendedResourceName`, Kueue unifies quota** using the `deviceClassMappings` logical
  name as the quota key for both paths, preventing over-allocation.

### Risks and Mitigations

With DRAAdminAccess and DRAPrioritizedLists (both beta, default enabled in K8s 1.35), there is a
risk that effective tallying of resources will not be available until after allocation of these
resources by kube-scheduler.

In order to mitigate this risk, Kueue can take the following approach:
1. For DRAPrioritizedLists: all the mentioned device classes in the request will be counted against the quota
2. For DRAAdminAccess: This feature can only be enabled in admin namespace, therefore it should be skipped for being
   counted against ClusterQuota. This is a different stance than Kubernetes ResourceQuota because kubernetes
   ResourceQuota is namespace scoped. As a result, admin users can account for quota independent of user workloads.
   On the contrary, with Kueue, since quota is part of ClusterQuota a cluster scoped object, admin workloads using
   devices in admin namespace if counted against quota, will eat up quota meant for user workloads.
3. For ResourceClaims with allocation mode `All`: worst-case scenario of the max number of devices that could be
   allocated to a single claim will be used against quota.
4. For Extended Resources: if a DeviceClass is created or updated between Kueue admitting a
   workload and kube-scheduler scheduling it, the two components may pick different DeviceClasses
   for the same `extendedResourceName` (a TOCTOU gap). This can happen during valid operational
   scenarios. KEP-5004 documents a transition pattern where two DeviceClasses temporarily
   coexist (create new class, then clear old mapping), and the scheduler picks the newer one.
   There are two failure modes:
   - **Scheduling failure**: the scheduler cannot allocate devices. `waitForPodsReady` catches
     this by timing out the Pending pod and evicting/re-queuing the workload.
     Users deploying DRA with Kueue should enable `waitForPodsReady`.
   - **Quota drift**: the scheduler allocates from a different DeviceClass than Kueue charged
     quota against, but the pod runs successfully. `waitForPodsReady` does not catch this.
     Since the extended resources path uses `extendedResourceName` directly as the quota key,
     quota accounting remains correct at the resource name level, though not at the physical
     DeviceClass level.
   To mitigate:
   - Kueue uses a controller-runtime field indexer on `DeviceClass` by `spec.extendedResourceName`
     to resolve DeviceClasses deterministically.
   - Per KEP-5004, admins should ensure one `extendedResourceName` maps to at most one
     DeviceClass.
   - Post-scheduling quota reconciliation will be evaluated for Beta.
   - TAS + DRA is the longer-term path to closing this admission-scheduling gap.

## Design Details

Two feature gates control DRA support in Kueue:
- `DynamicResourceAllocation`: gates ResourceClaimTemplate-based DRA quota accounting.
  Uses `deviceClassMappings` for DeviceClass-to-quota-resource mapping.
- `DRAExtendedResources`: gates extended resources support, including DeviceClass
  auto-discovery via `extendedResourceName`. Does not use `deviceClassMappings`.
  Requires `DynamicResourceAllocation` to also be enabled.

The following sections will explain the design in detail.

### Configuration API Extension for DRA

DRA device class mappings are configured through the existing Kueue Configuration API rather than a standalone CRD.
This approach provides a centralized configuration mechanism and avoids the complexity of managing additional CRDs.

```golang
// Resources struct in the Configuration API
type Resources struct {
    // DeviceClassMappings defines mappings from device classes to logical resources
    // for Dynamic Resource Allocation support.
    // +optional
    DeviceClassMappings []DeviceClassMapping `json:"deviceClassMappings,omitempty"`
}

// DeviceClassMapping holds device class to logical resource mappings
// for Dynamic Resource Allocation support.
type DeviceClassMapping struct {
    // Name is referenced in ClusterQueue.nominalQuota and Workload status.
    // Must be a valid fully qualified name consisting of an optional DNS subdomain prefix
    // followed by a slash and a DNS label, or just a DNS label.
    // DNS labels consist of lower-case alphanumeric characters or hyphens,
    // and must start and end with an alphanumeric character.
    // DNS subdomain prefixes follow the same rules as DNS labels but can contain periods.
    // The total length must not exceed 253 characters.
    Name corev1.ResourceName `json:"name"`

    // DeviceClassNames enumerates the DeviceClasses represented by this resource name.
    // Each device class name must be a valid qualified name consisting of an optional DNS subdomain prefix
    // followed by a slash and a DNS label, or just a DNS label.
    // DNS labels consist of lower-case alphanumeric characters or hyphens,
    // and must start and end with an alphanumeric character.
    // DNS subdomain prefixes follow the same rules as DNS labels but can contain periods.
    // The total length of each name must not exceed 253 characters.
    DeviceClassNames []corev1.ResourceName `json:"deviceClassNames"`
}
```

The cluster admin defines the mappings from device classes to logical resource names, which can then be used to
define quotas in ClusterQueues.

**Configuration Example:**
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kueue-controller-manager-config
  namespace: kueue-system
data:
  config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta2
    kind: Configuration
    namespace: kueue-system
    manageJobsWithoutQueueName: false
    resources:
      deviceClassMappings:
      - name: whole-gpus
        deviceClassNames:
        - gpu.example.com
      - name: shared-gpus
        deviceClassNames:
        - ts-shard-gpus.example.com
        - sp-shared-gpus.example.com
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ResourceFlavor
metadata:
  name: "default-gpu-flavor"
spec:
  # No changed needed here
---
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "gpus-cluster-queue"
spec:
  resourceGroups:
  - coveredResources: ["cpu", "memory", "whole-gpus", "shared-gpus"]
    flavors:
    - name: "default-gpu-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: "1200Mi"
      - name: 'whole-gpus'
        nominalQuota: 2
      - name: 'shared-gpus'
        nominalQuota: 2
```

The above ClusterQueue is an example configuration of a queue, with half quota configured for single allocation of
example
GPUs, and half quota configured for GPUs that are shared by workloads. Similarly, when DRAPartitionableDevices feature
is supported in kubernetes, GPUs partitions can be represented by a single device class.

### Device Class Resolution and Conflict Prevention

#### Device Class Mapping Uniqueness

To ensure predictable and deterministic quota enforcement, Kueue enforces strict uniqueness constraints on device class
mappings. Each device class can only map to one resource name across all device class mappings in the configuration.

Kueue prevents ambiguous configurations through validation at configuration load time. The following configuration
would be rejected:

```yaml
# INVALID - This configuration will be rejected during validation
apiVersion: v1
kind: ConfigMap
metadata:
  name: kueue-controller-manager-config
  namespace: kueue-system
data:
  config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta2
    kind: Configuration
    resources:
      deviceClassMappings:
      - name: whole-gpus
        deviceClassNames:
        - gpus.example.com          # Appears here
      - name: fast-gpus
        deviceClassNames:
        - gpus.example.com          # ERROR: Duplicate device class name
```

Example of valid configuration:
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: kueue-controller-manager-config
  namespace: kueue-system
data:
  config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta2
    kind: Configuration
    resources:
      deviceClassMappings:
      - name: whole-gpus
        deviceClassNames:
        - whole-gpus.example.com     # Unique device class
      - name: fast-gpus
        deviceClassNames:
        - fast-gpus.example.com      # Different device class
```

This validation approach eliminates ambiguity at configuration time rather than requiring complex runtime resolution logic, ensuring predictable and efficient workload admission.

### RBAC Requirements

DRA support requires additional RBAC permissions for the Kueue controller to access DRA resources:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kueue-controller-role
rules:
# ... existing permissions ...

# DRA-specific permissions
- apiGroups: ["resource.k8s.io"]
  resources: ["resourceclaims"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["resource.k8s.io"]
  resources: ["resourceclaimtemplates"]
  verbs: ["get", "list", "watch"]
- apiGroups: ["resource.k8s.io"]
  resources: ["deviceclasses"]
  verbs: ["get", "list", "watch"]
```

**Required Permissions:**
- `resourceclaims`: Read access to validate ResourceClaim references (though not supported for quota)
- `resourceclaimtemplates`: Read access to process ResourceClaimTemplates and extract device class information
- `deviceclasses`: Read access to look up `extendedResourceName` for Extended Resources

**Security Considerations:**
- Kueue only requires read permissions - no create, update, or delete access to DRA resources
- Permissions are cluster-scoped to allow processing workloads across all namespaces
- No elevated privileges required beyond standard Kueue controller permissions

### Workloads

#### DRA-Specific Workload Processing

DRA workloads require special handling to ensure proper resource validation and quota enforcement. Unlike standard
workloads that are processed immediately in event handlers, DRA workloads are processed in the controller's Reconcile
loop to enable proper error handling and retry logic.


1. Event Handler Behavior: When a DRA workload is created or updated, the event handlers detect the presence of
   ResourceClaimTemplates or ResourceClaims and if feature gate is enabled, skip normal queue operations, deferring
   processing to the Reconcile loop.
2. Reconcile Loop Processing: The Reconcile method handles all DRA-specific logic including:
   - Feature gate validation
   - ResourceClaim vs ResourceClaimTemplate support validation
   - Device class mapping resolution
   - Resource preprocessing and queue admission
3. Error Handling: DRA processing errors are properly handled with exponential backoff retry logic.

#### Workload Processing Flow

When a user submits a workload and DynamicResourceAllocation feature gate is enabled, Kueue processes it as follows:
1. DRA Detection: Kueue detects DRA workloads by checking for ResourceClaimTemplates or ResourceClaims in
   podSpec.resourceClaims.
2. Feature Gate Validation: Verify that the DynamicResourceAllocation feature gate is enabled. If disabled, continue
   with legacy behavior.
3. ResourceClaim Support Validation: Check if the workload uses ResourceClaims (not supported in alpha) or
   ResourceClaimTemplates (supported):
   - ResourceClaims: Mark workload as inadmissible with an error message
   - ResourceClaimTemplates: Continue processing
4. Device Class Resolution: For each ResourceClaimTemplate:
   - Read the ResourceClaimTemplate from the same namespace as the workload
   - Extract deviceClassName from each request in the template spec
   - Look up the corresponding resource name using the device class mappings from the Configuration API
5. Resource Preprocessing:
   - Calculate total device count per device class across all containers and init containers
   - Map device classes to resource names using the configuration
   - Generate preprocessed resource requests for queue admission
6. Queue Admission: Add the workload to the queue with preprocessed DRA resources.
7. Status Update: Once admitted, the workload status reflects the assigned flavors and resource usage including DRA resources.

Note: The flow above applies to the ResourceClaimTemplate path (`DynamicResourceAllocation` gate).
When the `DRAExtendedResources` gate is also enabled, workloads with extended resources in
`resources.requests` follow a separate resolution path through the ExtendedResourceCache.
See [Extended Resources](#extended-resources) for details. Both paths can be active simultaneously
for workloads that use both ResourceClaimTemplates and extended resources.

The steps above are reflected in the complete configuration and workload example below:

```yaml
# Step 1: Configure device class mappings in Kueue Configuration
apiVersion: v1
kind: ConfigMap
metadata:
  name: kueue-controller-manager-config
  namespace: kueue-system
data:
  config.yaml: |
    apiVersion: config.kueue.x-k8s.io/v1beta2
    kind: Configuration
    namespace: kueue-system
    resources:
      deviceClassMappings:
      - name: whole-gpus
        deviceClassNames:
        - gpu.example.com # Maps gpu.example.com -> whole-gpus
---
# Step 2: Define ClusterQueue with DRA resource quotas
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: "gpus-cluster-queue"
spec:
  resourceGroups:
  - coveredResources: ["cpu", "memory", "whole-gpus"]
    flavors:
    - name: "default-gpu-flavor"
      resources:
      - name: "cpu"
        nominalQuota: 9
      - name: "memory"
        nominalQuota: "1200Mi"
      - name: 'whole-gpus' # References the resource name from Configuration
        nominalQuota: 2
---
# Step 3: Create ResourceClaimTemplate (only templates are supported)
apiVersion: resource.k8s.io/v1
kind: ResourceClaimTemplate
metadata:
  namespace: gpu-test1
  name: single-gpu
spec:
  spec:
    devices:
      requests:
      - name: gpu
        exactly:
          deviceClassName: gpu.example.com # Device class from the mapping
---
# Step 4: Submit workload using ResourceClaimTemplate
apiVersion: batch/v1
kind: Job
metadata:
  namespace: gpu-test1
  name: job0
  labels:
    app: job
    kueue.x-k8s.io/queue-name: user-queue
spec:
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: ctr0
        image: ubuntu:22.04
        command: ["bash", "-c"]
        args: ["export; sleep 9999"]
        resources:
          claims:
          - name: gpu # Reference to the resource claim
          requests:
            cpu: 1
            memory: "200Mi"
      resourceClaims:
      - name: gpu
        resourceClaimTemplateName: single-gpu # Must use template, not direct claim
---
# Step 5: Resulting Workload status after admission
apiVersion: kueue.x-k8s.io/v1beta2
kind: Workload
metadata:
  name: job-job0-6f46e
  namespace: gpu-test1
# ... spec omitted for brevity ...
status:
  admission:
    clusterQueue: gpus-cluster-queue
    podSetAssignments:
    - count: 1
      flavors:
        cpu: default-gpu-flavor
        memory: default-gpu-flavor
        whole-gpus: default-gpu-flavor # DRA resource assigned to flavor
      name: main
      resourceUsage:
        cpu: "1"
        memory: "200Mi"
        whole-gpus: "1" # DRA device count reflected in status
```

### Extended Resources

This section is gated behind the `DRAExtendedResources` Kueue feature gate.

Kueue also supports workloads requesting DRA devices via `resources.requests` (e.g., `example.com/gpu: 1`).
When a DeviceClass has `spec.extendedResourceName` set, kube-scheduler automatically creates ResourceClaims.
This requires the Kubernetes `DRAExtendedResource` feature gate (alpha in k8s 1.35).

An extended resource can be identified by verifying that qualified resource names containing `/` are not in the `kubernetes.io/` or `requests.` namespaces and are not standard resources like `cpu`, `memory`, `ephemeral-storage`, or `hugepages-*`.

#### Configuration

The extended resources path does not require `deviceClassMappings`. Kueue auto-discovers
DeviceClasses via a field indexer on `spec.extendedResourceName` and uses the
`extendedResourceName` directly as the quota key in ClusterQueue.

DeviceClass with `extendedResourceName` (DeviceClass API is v1/GA, but the `extendedResourceName`
field requires the Kubernetes `DRAExtendedResource` feature gate):
```yaml
apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: gpu.example.com
spec:
  extendedResourceName: example.com/gpu
```

ClusterQueue uses the `extendedResourceName` directly as the quota resource:
```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: gpu-queue
spec:
  resourceGroups:
  - coveredResources: ["example.com/gpu"]
    flavors:
    - name: default
      resources:
      - name: example.com/gpu
        nominalQuota: 8
```

No Kueue configuration changes are needed. No `deviceClassMappings` entry is required for
extended resources. This is a clean separation from the ResourceClaimTemplate path, which
continues to use `deviceClassMappings`.

#### Path Separation

The two DRA paths have independent quota resolution:
1. **Extended resources** (`DRAExtendedResources` gate): auto-discovers DeviceClass via
   field indexer, uses `extendedResourceName` as quota key. No `deviceClassMappings` needed.
2. **ResourceClaimTemplates** (`DynamicResourceAllocation` gate): uses `deviceClassMappings`
   to map DeviceClass names to logical resource names. No auto-discovery.

There is no precedence or fallback between the two paths. Each path has exactly one resolution
mechanism.

#### Processing Flow

1. Kueue detects extended resources in `resources.requests`
2. Looks up DeviceClass by `extendedResourceName` by field indexer
3. If no matching DeviceClass is found, the resource is not DRA-backed and Kueue
   processes it through the standard resource quota path (counted via `node.Status.Allocatable`)
4. If a matching DeviceClass is found, uses the `extendedResourceName` as the quota key
5. Removes original extended resource from the workload's effective resource requests
   (tracked internally per PodSet) to avoid double-counting
6. Admits workload against the quota for the `extendedResourceName`

The extended resource translation reads directly from the workload spec before
`excludeResourcePrefixes` filtering is applied. The processing order:
1. Extended resource translation runs first, reading the original spec
2. `excludeResourcePrefixes` filters the pod's `resources.requests`
3. Original extended resource is removed from the workload's effective resource requests
4. Translated resource is added through `preprocessedDRAResources`

This ensures no overlap or double-counting between the two mechanisms.

#### Same Hardware with Both Paths

When the same hardware needs to serve both ResourceClaimTemplate users and extended resource
users, admins configure separate flavors under the same ClusterQueue. Assuming a cluster
with 1 node and 8 GPU devices available:

```yaml
# DeviceClass
apiVersion: resource.k8s.io/v1
kind: DeviceClass
metadata:
  name: gpu.example.com
spec:
  extendedResourceName: example.com/gpu
---
# Kueue config: deviceClassMappings only needed for ResourceClaimTemplate path
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
resources:
  deviceClassMappings:
  - name: gpu-claims
    deviceClassNames:
    - gpu.example.com
---
# ClusterQueue with quota for both paths
apiVersion: kueue.x-k8s.io/v1beta2
kind: ClusterQueue
metadata:
  name: gpu-queue
spec:
  resourceGroups:
  - coveredResources: ["example.com/gpu", "gpu-claims"]
    flavors:
    - name: default
      resources:
      - name: example.com/gpu    # for extended resource users
        nominalQuota: 4
      - name: gpu-claims          # for ResourceClaimTemplate users
        nominalQuota: 4
```

Both quota buckets draw from the same physical hardware. The admin controls how capacity is
split between the two user populations. Since these are different resource names, the split
is fixed at configuration time.

#### DeviceClass Resolution via Field Indexer

Kueue resolves `extendedResourceName` to DeviceClasses using a controller-runtime field indexer
on `DeviceClass` by `spec.extendedResourceName`. This provides fast lookups without adding
dependencies on non-staging k8s repos.

Even when multiple DeviceClasses share the same `extendedResourceName` (which K8s
[permits with deterministic tiebreaking](https://github.com/kubernetes/kubernetes/blob/v1.35.0/staging/src/k8s.io/api/resource/v1/types.go#L1816-L1820)),
Kueue still treats the resource as DRA-backed. The quota key is the `extendedResourceName`
itself, not the DeviceClass name, so multiple matching DeviceClasses do not affect quota accounting.

#### DeviceClass Lifecycle Scenarios

1. Two DeviceClasses with the same `extendedResourceName`: per KEP-5004, admins should ensure
   one `extendedResourceName` maps to at most one DeviceClass.

2. New DeviceClass created after Kueue admits a workload: a TOCTOU gap exists where Kueue
   and the scheduler may resolve differently. `waitForPodsReady` handles scheduling failures.
   See [Risks and Mitigations](#risks-and-mitigations) for the full breakdown.

3. DeviceClass `extendedResourceName` updated: the field indexer reflects the updated mapping
   on the next reconciliation cycle. The same TOCTOU considerations as scenario 2 apply.

#### Late DeviceClass Creation

For Alpha, if a DeviceClass does not exist when a workload is created, the extended resource
is treated as a normal (non-DRA) extended resource. A subsequent reconciliation cycle
(e.g., triggered by other workload or queue events) re-evaluates the workload after the
DeviceClass is created. Event-driven re-reconciliation on DeviceClass creation will be
evaluated as a Beta graduation criterion.

### Architecture Details

#### Queue Manager Extensions

The queue manager has been extended to support DRA resource preprocessing through the InfoOption pattern:

```golang
// Extended method signatures
func (m *Manager) AddOrUpdateWorkload(wl *kueue.Workload, opts ...workload.InfoOption) error
func (m *Manager) UpdateWorkload(oldWl, newWl *kueue.Workload, opts ...workload.InfoOption) error

// DRA-specific InfoOption
func WithPreprocessedDRAResources(
	draResources map[kueue.PodSetReference]corev1.ResourceList,
	replacedExtendedResources map[kueue.PodSetReference]sets.Set[corev1.ResourceName],
) workload.InfoOption
```

Processing Flow:
1. DRA Preprocessing: Controller processes ResourceClaimTemplates and calculates resource requirements
2. InfoOption Creation: Preprocessed resources are wrapped in `WithPreprocessedDRAResources` option
3. Queue Integration: Queue manager receives workload with preprocessed DRA data
4. Scheduler Access: Scheduler gets workload with DRA resources already calculated and validated

This architecture separates concerns between DRA processing (controller) and queue management (scheduler), enabling robust error handling and retry logic for DRA-specific operations.

### MultiKueue Integration

DRA workloads are supported with MultiKueue through the existing workload synchronization mechanism. ResourceClaimTemplates must be deployed on worker clusters by users; they are not automatically synced.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

##### Prerequisite testing updates

<!--
Based on reviewers feedback describe what additional tests need to be added prior
implementing this enhancement to ensure the enhancements have also solid foundations.
-->

#### Unit Tests

<!--
In principle every added code should have complete unit test coverage, so providing
the exact set of tests will not bring additional value.
However, if complete unit test coverage is not possible, explain the reason of it
together with explanation why this is acceptable.
-->

<!--
Additionally, try to enumerate the core package you will be touching
to implement this enhancement and provide the current unit coverage for those
in the form of:
- <package>: <date> - <current test coverage>

This can inform certain test coverage improvements that we want to do before
extending the production code to implement this enhancement.
-->
- pkg/cache/queue/manager.go: 09/17/2025 - 61.5%
- pkg/config/validation.go: 09/17/2025 - 97.3%
- pkg/controller/core/workload_controller.go: 09/17/2025 - 55.8%
- pkg/dra/claims.go: 09/17/2025 - 83.3%
- pkg/dra/extended_resources.go: TODO (pkg/dra overall: 89.6%)
- pkg/workload/workload.go: 09/17/2025 - 72.3%

#### Integration tests

Integration tests in Kueue use controller-runtime's envtest framework, which provides a real Kubernetes API server
without requiring kubelet or other cluster components. While DRA device allocation requires kubelet plugins, the core
DRA integration functionality for Kueue can be tested at the integration level by:

- Testing Configuration API validation for device class mappings
- Verifying workload admission logic with DRA ResourceClaimTemplates
- Testing quota enforcement against device class mappings
- Validating resource counting and flavor assignment for DRA resources
- Testing error scenarios (feature gate disabled, unsupported ResourceClaims, unmapped device classes)
- Verifying DRA workload processing in Reconcile loop vs event handlers

The integration tests focus on Kueue's quota management and admission logic rather than actual device allocation,
using mock ResourceClaimTemplates and DeviceClasses to simulate DRA workloads. Key test scenarios include:

- Configuration validation: Testing device class conflict detection
- Workload inadmissibility: Testing various error conditions and proper WorkloadInadmissible condition setting
- Resource preprocessing: Verifying correct device count calculation from ResourceClaimTemplates
- Queue integration: Testing workload admission with preprocessed DRA resources
- Extended Resources: Testing extended resource detection, DeviceClass lookup, and resource translation
- Late DeviceClass creation: Testing workload inadmissibility when DeviceClass does not exist

#### E2E Test

Use existing dra-example-driver or Kubernetes test driver for e2e testing.

### Graduation Criteria

#### Alpha

- the implementation behind the feature gate flag in alpha
- support v1 API of DRA in core k8s
- initial e2e tests for baseline scenario
- support for Extended Resources (alpha in k8s 1.35)

#### Beta

- the feature gate in Beta
- all known bugs are fixed
- support integration with MultiKueue
- e2e tests
- TAS + DRA testing and support as a graduation requirement
- re-evaluate event-driven DeviceClass tracking for late DeviceClass creation
- re-evaluate post-scheduling quota reconciliation for DeviceClass drift
- re-evaluate the support for TopologyAwareScheduling (might be moved to GA)

#### GA

- the feature gate in stable
- integration with TopologyAwareScheduling

## Implementation History

- Initial draft on September 16th 2024 by @kannon92
- Implementation development: September-December 2024
- Design evolution from standalone CRD to Configuration API approach: October 2024
- Alpha implementation completed: December 2024
- KEP updated to reflect actual implementation: September 2025 by @alaypatel07
- Extended Resources implementation: January 2026

**Key Design Evolution:**
- **Original Design**: Standalone DynamicResourceAllocationConfig CRD with runtime ambiguity resolution
- **Final Implementation**: Configuration API extension with strict validation and conflict prevention
- **Architecture Decision**: DRA processing moved to Reconcile loop for proper error handling
- **Scope Refinement**: ResourceClaims support removed, focus on ResourceClaimTemplates only
- **Extended Resources**: Added support for workloads requesting DRA devices via `resources.requests`
  using DeviceClass `extendedResourceName` field (alpha in k8s 1.35)

## Drawbacks

**Configuration Restart Requirement**: Changes to device class mappings require controller restart, which may cause brief service interruption. This is acceptable for alpha feature but should be addressed in future versions.

**ResourceClaims Not Supported**: Users with existing ResourceClaim-based workloads cannot use Kueue quota management and must migrate to ResourceClaimTemplates.

**Limited Dynamic Reconfiguration**: Unlike some other Kueue features, DRA configuration cannot be changed dynamically and requires controller restart.

## Alternatives

### Webhook Rewriting Extended Resources to ResourceClaimTemplates

For extended resources support, an alternative approach was considered: use Kueue's existing
mutating webhook to rewrite extended resource requests (e.g., `example.com/gpu: 1`) into
ResourceClaimTemplate references at admission time. This would eliminate the need for a
separate DeviceClass resolution path in Kueue, since the existing ResourceClaimTemplate
processing would handle quota accounting.

This approach was rejected for several reasons:
1. Creating a ResourceClaimTemplate from a webhook is a side effect, violating the
   `sideEffects: None` declaration on Kueue webhooks.
2. Late DeviceClass creation cannot be handled. If the DeviceClass does not exist when the
   webhook fires, the webhook must either reject the workload (creating an ordering
   dependency on admin configuration) or pass it through unchanged (requiring a controller
   fallback that duplicates the logic).
3. Webhook ordering and reinvocation issues with external frameworks that modify pod specs
   after Kueue's webhook runs.
4. DRA processing in Kueue follows the pattern of handling logic in the Reconcile loop
   rather than event handlers or webhooks, enabling proper error handling and retry.
5. It goes against the architectural direction of reducing webhook surface area in Kueue.

### ResourceClaim By Count

Keeping a tally of the resource claims for a given workload could be another mechanism for enforcing quota.
However, the issue with this is that resource claims are namespaced scoped, to enforce quota usage across namespaces
kueue need to rely on a cluster-scope resource.

Additionally, ResourceClaims capture the intent of the user on what kind of device is request. The request could mean
anything from one small allocatable device to several devices or entire resource pool. Therefore, tracking the number
of requests becomes non-intuitive. The need is to count devices going to be allocated to those requests.

### Using devices in ResourceSlice to Count

DRA drivers publish resources for each node, which could be used as a mechanism for counting resources. However, in DRA
implementation, ResourceSlices are used for driver/scheduler communication. The only way users can request dynamic
resources is via ResourceClaims. ResourceClaims does not have the notion of what devices will be allocated a priori.

Enforcing quota requires two inputs, 1) user request and 2) system usages. With using ResourceSlice, the first
requirement
is missing.

### Using a CEL expression

Cluster admin might have to create new deviceclass for narrowing set of target devices in existing device class for
setting quota. Moreover, when existing users use the old device classes, they might have to migrate to the new
deviceclass.
For example, assume gpu.example.com deviceclass exists, and each device has device attribute "memory" there are existing
users who have resourceclaims with the deviceclass and selector like this:
```yaml
kind: ResourceClaim
name: one-large-gpu
spec:
  devices:
    requests:
    - name: gpu-large
      exactly:
        deviceClassName: gpu.example.com
        selectors:
        - cel:
            expression: device.attributes["memory"] >= 80g
```

Now, if Kueue admin wants to set quota for gpu.example.com devices with device.attribute["memory"]>=80g, Kueue admin
might have to create a device class and use the new device class in clusterqueue:

```yaml
kind: DeviceClass
name: large-gpu.example.com
spec:
  selectors:
  - cel:
      expression: device.driver == "gpu.example.com" && device.attributes["memory"] >= 80g
```

Then, existing users might have to migrate/switch their ResourceClaim with large-gpu.example.com device class from
existing one.

For minimizing user impact, there could be an API change that allows CEL expression along with deviceclass name in
defining the Kueue quota, like this:
```yaml
kind: Device
nominalQuota: 2
devices:
  # We might be able to extend this object to support
  # partitionable devices, etc. in the future??
- className: gpu.example.com
  selectors:
  - cel:
      expression: device.attributes["memory"] >= 80g
```

This indeed improves the user experience, but with a cel expression like that, whether a device having attributes that
evaluates the cel expression to true or not, will only be available after the scheduler allocates the device for the
claim. Kueue needs to know the device and count it before admitting the workload and hence before it hits the
kube-scheduler. Any inclusion relationship between two boolean formulae in ResourceClaim and ClusterQueue cannot be
assumed.

For example, assume the following ResourceClaim and ClusterQueue exist. In this situation, it is clear that there could
be both cases where the allocation result consumes and does not consume the quota (i.e. this means we have to wait for
the allocation result).

```yaml
kind: ResourceClaim
name: one-mid-or-large-gpu
spec:
  devices:
    requests:
    - name: middle-or-large-gpu
      exactly:
        deviceClassName: gpu.example.com
        selectors:
        - cel:
            expression: 50g < device.attributes["memory"] and device.attributes["memory"] <= 100g
---
kind: Device
devices:
- className: gpu.example.com
  selectors:
  - cel:
      expression: device.attributes["memory"] <= 80g
```

### Defining DeviceClass mapping in ClusterQuota

The definition of what DeviceClasses construct a DRA device could be in ClusterQuota just before declaring the nominal
count for the device.

```golang
type DynamicResourceMapping struct {
	// Name is the resource name of this mapping. This will be referred in ClusterQueue
	// and Workload status
	Name corev1.ResourceName `json:"name"`

	// deviceClassNames lists the names of all the device classes that will count against
	// the quota defined in this resource
	// +listType=atomic
	DeviceClassNames []corev1.ResourceName `json:"deviceClassNames"`
}

type ResourceFlavorSpec struct {
	// dynamicResources defines Kubernetes Dynamic Resource Allocation resources
	// +optional
	// +featureGate=DynamicResourceStructuredParameters
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	DynamicResources []DynamicResourceMapping `json:"dynamicResources,omitempty"`
}
```

This presents a problem where the same resource name could be used to define DeviceClasses A and B in one ClusterQueue
and DeviceClasses C, D and E in another ClusterQueue leading to conflicts. Since the mapping resource name to list of
DeviceClasses is not shared, it is hard to implement borrowing as it becomes very non-deterministic. Hence, this
approach
is not feasible.

### Using ResourceFlavor for DeviceClass Mapping

An earlier design considered embedding device class mappings directly in the ResourceFlavor API instead of creating
a separate DynamicResourceAllocationConfig CRD:

```golang
type DynamicResourceMapping struct {
	// Name is the resource name of this mapping. This will be referred in ClusterQueue
	// and Workload status
	Name corev1.ResourceName `json:"name"`

	// deviceClassNames lists the names of all the device classes that will count against
	// the quota defined in this resource
	// +listType=atomic
	DeviceClassNames []corev1.ResourceName `json:"deviceClassNames"`
}

type ResourceFlavorSpec struct {
	// dynamicResources defines Kubernetes Dynamic Resource Allocation resources
	// +optional
	// +featureGate=DynamicResourceStructuredParameters
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	DynamicResources []DynamicResourceMapping `json:"dynamicResources,omitempty"`
}
```

However, this design had a major drawback. The biggest issue was semantic confusion - a dynamicResource like `gpu` in
ResourceFlavor1 could have deviceClass gpu-a.example.com while the same dynamicResource name in ResourceFlavor2 could
have the completely different deviceClass gpu-b.example.com. This creates significant confusion for cluster
administrators because the same resource name would have different meanings depending on which ResourceFlavor was being
referenced. The singleton DynamicResourceAllocationConfig CRD approach addresses this by providing a single source of
truth for all device class mappings in the cluster.

### Creating a new CRD for device class mapping

```golang
// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// DynamicResourceAllocationConfig is a singleton CRD that maps resource names to device classes
// used in ClusterQueue resource quotas. It is singleton as "default" is the only allowed named for the CRD instance in
// Kueue namespace.
type DynamicResourceAllocationConfig struct {
    metav1.TypeMeta   `json:",inline"`
    metav1.ObjectMeta `json:"metadata,omitempty"`
    // Spec defines the desired state of DynamicResourceAllocationConfig
    Spec DynamicResourceAllocationConfigSpec `json:"spec"`
}
// DynamicResourceAllocationConfigSpec defines the mappings between resource names and device classes
type DynamicResourceAllocationConfigSpec struct {
    // Resources is a list of mappings from resource name to device classes
    // +listType=map
    // +listMapKey=name
    // +kubebuilder:validation:MaxItems=16
    Resources []DynamicResource `json:"resources"`
}
// DynamicResource defines a mapping from a resource name to a list of device classes
type DynamicResource struct {
    // Name is the resource name that will be referred to in ClusterQueue and Workload admission status.
    Name corev1.ResourceName `json:"name"`
    // DeviceClassNames lists the names of all the device classes that will count against
    // the quota defined for this resource name
    // +listType=set
    // +kubebuilder:validation:MaxItems=8 
    DeviceClassNames []corev1.ResourceName `json:"deviceClassNames"`
}
```
However, this approach was introducing a significant amount of complexity in implementing the feature so it was rejected.
