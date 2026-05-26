# KEP-2349: MultiKueue external custom Job support

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
  - [Constraints](#constraints)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Risks](#risks)
    - [Mitigations](#mitigations)
- [Design Details](#design-details)
  - [Configurable MultiKueue Adapters](#configurable-multikueue-adapters)
    - [Job Flow for the Generic Adapter](#job-flow-for-the-generic-adapter)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary
This KEP introduces a mechanism to support MultiKueue for custom Jobs. This is achieved by introducing a configurable system that allows administrators to choose between a built-in generic MultiKueue adapter or an external controller for managing custom job types.

## Motivation

Many Kueue adopters have specific CustomResources which might not be public.
Kueue should give a possibility to manage such custom Jobs across multiple clusters.

### Goals

Give a possibility for external controllers to manage custom Jobs in a MultiKueue environment.


### Non-Goals

This KEP does not aim to generalize the Workload synchronization mechanism for kueue-external-controller.
Even after this feature is supported, the Workload resource synchronization is handled by kueue-controller-manager (MultiKueue).
## Proposal

This KEP proposes extending MultiKueue to support custom jobs through a configurable mechanism. Administrators will be able to define, for each custom job type (identified by its GroupVersionKind), whether it should be handled by a built-in generic adapter or by an external, multi-cluster-aware controller.

This configuration will be managed via a configmap. Based on this configuration, MultiKueue will either use its generic adapter for a given GVK or expect an external controller to manage the job's lifecycle across clusters, in which case MultiKueue will only be responsible for synchronizing the `Workload` object.

### User Stories

As a cluster administrator I would like to have the ability to schedule Jobs
which aren't natively supported by Kueue, eg Tekton.

### Constraints

- Fixed Transformation: The generic adapter is limited to a fixed, non-configurable transformation logic.

### Risks and Mitigations

#### Risks

 Configmaps are unstructured by default. They don't provide validation by default unlike CRD. User could make typo mistakes or give invalid syntax in configmap.

#### Mitigations

Configmaps that are not correct should be logged with errors like "Invalid GVK format". We could have a CLI command to validate the adapter logic.

## Design Details

### Configurable MultiKueue Adapters

To support external frameworks, the MultiKueue configuration API will be extended. Administrators will be able to list custom job GVKs that should be handled by a built-in generic adapter.

The following definitions will be added to the `apis/config/v1beta1` package:

```go
// MultiKueue specifies the multikueue configuration options.
type MultiKueue struct {
   .....
	// ExternalFrameworks is the list of the external frameworks to be supported by the generic MultiKueue adapter.
	// +optional
	ExternalFrameworks []MultiKueueExternalFramework `json:"externalFrameworks,omitempty"`
}

// MultiKueueExternalFramework defines a framework that is not built-in.
type MultiKueueExternalFramework struct {
	// Name is the GVK of the resource that are
    // is managed by external controllers
    // the expected format is `Kind.version.group.com`.
	Name string `json:"name"`
}
```

This change will be reflected in the `kueue-manager-config` configmap.

Example YAML configuration:

```yaml
     # In the multiKueue Kueue Config
     multiKueue:
       externalFrameworks:
         - name: "pipelinerun.v1.tekton.dev"
```

For every entry in `multiKueue.externalFrameworks`, we register a generic MultiKueue adapter. This adapter performs a default set of operations to manage the job on a worker cluster. It assumes a `.spec.managedBy` field in the custom job and will handle removing it before creation on the worker, as well as syncing the `/status` field back from the worker to the management cluster.

The adapter implements the [MultiKueueAdapter interface](https://github.com/kubernetes-sigs/kueue/blob/22e4502335b0116300b3dcc63d237cf8b7ac759a/pkg/controller/jobframework/interface.go#L235C5-L235C6) as follows:
- *IsJobManagedByKueue*: Reads the local job object and uses a default JSONPath (`.spec.managedBy`) to verify it's managed by MultiKueue.
- *SyncJob (Create)*: Clones the local job object, removes the `.spec.managedBy` field, adds the standard MultiKueue labels (`prebuilt-workload`, `multikueue-origin`), and creates the resulting object on the worker cluster.
- *SyncJob (Status Sync)*: Fetches the local and remote job objects and syncs the `/status` field from the remote object to the local one.

A significant benefit of this approach is the potential to replace the implementation of several built-in adapters (e.g., `JobSet`, `RayJob`, `RayCluster`) by registering the generic adapter for their GVKs. This would lead to a reduction in specialized code and improve maintainability.

However, the generic adapter is not a universal solution. It cannot replace adapters for workloads that have unique lifecycle management mechanisms. This is important for maintainers of external frameworks to consider when choosing an implementation path. Notable exceptions include:

- **Job**: The current adapter relies on the `MultiKueueBatchJobWithManagedBy` environment variable. This is a temporary limitation until the `JobManagedBy` feature in Kubernetes reaches General Availability.
- **Pod**: The Pod adapter uses `schedulingGates` instead of the `managedBy` field. Extending the generic adapter to support this mechanism is out of scope for this KEP.

#### Job Flow for the Generic Adapter
   1. User: Submits the custom job (e.g., PipelineRun) with `queue-name` and `managedBy` fields to the management cluster.
   2. An external controller creates a `Workload` object for the job.
   3. MultiKueue admits the workload once quota is available in a worker, assigning it to that worker cluster.
   4. MultiKueue's Generic Adapter clones the job, applies default transformations (like removing `managedBy`), and creates it on the worker cluster.
   5. The Generic Adapter periodically fetches the job's status from the worker and applies it to the original job on the management cluster.
   6. When the original job is deleted, the MultiKueue adapter removes the corresponding job from the worker cluster.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.
- We will test the configmap parsing and validation logic.
- We will test the generic adapter logic.

##### Prerequisite testing updates

#### Unit Tests

Target is to keep same level or increase the coverage for all of the modified packages.
Cover the critical part of newly created code.

- `<package>`: `<date>` - `<test coverage>`

#### Integration tests

- A test will be added that creates a `ConfigMap` with rules for a mock GVK.
- We will then create a `Workload` for that mock GVK and verify that the reconciler correctly applies the creation and status sync logic for the generic adapter.
- A test will verify that a `Workload` for an unconfigured GVK is correctly rejected.

### Graduation Criteria

#### Alpha
- **Milestone:** v0.14
- **Feature Gate:** `MultiKueueAdaptersForCustomJobs`
- Implementation is behind a feature gate.
- The generic adapter for custom jobs is implemented.

#### Beta
- **Milestone:** v0.15
- All major reported bugs are fixed.
- The feature gate is enabled by default.
- Re-evaluate the need for supporting custom patches (for different placement of managedBy or status).
- Re-visit whether support for specifying location of managedBy field is required.
- Re-evaluate the need for ControllerType field under External Frameworks struct

#### GA

- All reported bugs are fixed.
- The feature gate is removed.


## Implementation History

* 2024-06-20 First draft
* 2025-07-15 Second draft
* 2025-11-11 Beta graduation


## Drawbacks


## Alternatives

1. Dedicated API for MultiKueue communication between admission check controller and abstraction layer controller.

Reasons to defer:
- overall complexity, requires a communication protocol like gRPC between MultiKueue AdmissionChecks and external controllers. 
- more difficult to debug as multiple layers are involved.
- requires to maintain compatibility between independently compiled binaries.

2. Don't do any code changes, a custom controller can: Instead of having MultiKueue cluster associated with controller name, change the Multikueue cluster status to use a custom active condition for old controllers (controller name) that use it.

Reasons to defer:
- The complexity of the external controllers which now need to essentially do the role of the MultiKueue Admission Checks, eg. managing the AC state, loading and using secrets

3. Introduce ability to create Multikueue Admission Check controllers with custom set of adapters.

Reasons to defer:
- it is much harder to use by users as they need to enlist multiple MultiKueue Amission Checks per CQ as initially proposed in https://github.com/kubernetes-sigs/kueue/pull/2458/files#diff-359284c3c284a904713acde8d57992e7dd5020400b955f25b990b7a35b712ae4R173-R175
- The external admission checks would need to largely repeat the code of the MultiKueue built-in controller
- Since the solution involves another big controller it is harder to maintain

4. Using AppWrapper: Using AppWrapper was considered as an alternative. However, this approach has several drawbacks for certain loads.

Reasons to defer:
   - Job requiring status synchronization: There is no out-of-the-box way to propagate the status of the wrapped resource from the worker cluster back to the management cluster. This would require writing a custom controller to handle status synchronization.
   - Abstraction Layer: It introduces an additional layer of abstraction. Users would need to wrap their custom resources (like Tekton PipelineRun) in an AppWrapper, which complicates the user experience.
   - PodSpec Requirement: AppWrapper might not be suitable for all custom resources, especially those that do not expose a PodSet in their spec.
   - Complexity: While a controller could be built to address some of these issues, it would increase the complexity of the overall solution, particularly for CI/CD use cases.

5. Configurable Controller Type for External Frameworks

An alternative approach considered was to allow administrators to specify a `controller` type for each external framework, choosing between the `generic` adapter and an `external` controller.

The configuration API would be extended as follows:

```go
// ControllerType is the type of controller for an external framework.
type ControllerType string

const (
	// GenericController is a built-in generic MultiKueue adapter.
	GenericController ControllerType = "generic"
	// ExternalController means an external, multi-cluster aware controller will manage the job.
	ExternalController ControllerType = "external"
)

// MultiKueueExternalFramework defines a framework that is not built-in.
type MultiKueueExternalFramework struct {
    .....
    // Controller is the type of controller for this framework. Defaults to "generic".
    // +optional
    ControllerType ControllerType `json:"controller_type,omitempty"`
}
```

In this model:
- `generic` (default): Would behave as described in the main proposal.
- `external`: Would expect an external, multi-cluster aware controller to manage the job. In this mode, MultiKueue would only sync the `Workload` object to the worker cluster. The external controller would be responsible for creating the job on the worker and syncing its status.

**Job Flow for an External Controller**
   1. User: Submits a custom job (like a Tekton PipelineRun) with a `queue-name` label.
   2. An external controller creates a corresponding `Workload` object.
   3. Kueue's scheduler decides when the `Workload` can be admitted based on available quota.
   4. Upon admission, MultiKueue's only action is to copy the `Workload` object to the designated worker cluster. It does not create the `PipelineRun` on the worker cluster.
   5. The external controller (e.g., Tekton's external kueue controller) needs to be "multi-cluster aware." It is responsible for:
       * Watching for `Workload` object updates on worker clusters.
       * When it sees a `Workload` get scheduled by MultiKueue, it then creates the original Job (e.g., PipelineRun) on the worker cluster to actually run the job.
       * It is also solely responsible for monitoring the `PipelineRun`'s status on the worker cluster and syncing it back to the `PipelineRun` object on the management cluster.

**Reasons to defer:**
This approach was deferred to simplify the initial implementation. The focus of this KEP is to provide a robust generic adapter first. Supporting external controllers can be revisited in the future if there is a strong demand for this functionality. It adds complexity to the configuration and implementation that is not necessary for the primary goal of supporting common custom jobs.
