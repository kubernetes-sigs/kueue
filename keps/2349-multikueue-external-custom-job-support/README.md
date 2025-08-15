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
  - [Generic Adapter via ConfigMap](#generic-adapter-via-configmap)
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
 This KEP introduces a mechanism to support MultiKueue for custom Jobs.  This introduces a generic, built-in MultiKueue adapter that is configurable via the Kueue configmap.

## Motivation

Many Kueue adopters have specific CustomResources which might not be public.
Kueue should give a possibility to manage such custom Jobs across multiple clusters.

### Goals

Give a possibility for external controllers to manage custom Jobs in a MultiKueue environment.


### Non-Goals

## Proposal

MultiKueue controller will be configurable via configmap. Based on this configmap, a generic MultiKueueAdapter will be implemented within Kueue. When processing a workload without supported built-in adapter, generic MultiKueueAdapter will be called based on GroupVersionKind (GVK) defined in configmap. It will use configuration defined to manage lifycycle's on the worker clusters.

### User Stories

As a cluster administrator I would like to have the ability to schedule Jobs
which aren't natively supported by Kueue, eg Tekton.

### Constraints

- Limited Transformation: Generic Adaptor are limited to simple JsonPatch transformation configurable.

### Risks and Mitigations

#### Risks

 Configmaps are unstructured by default. They don't provide validation by default unlike CRD. User could make typo mistakes or give invalid syntax in configmap. Or user could give invalid syntax.

#### Mitigations

Configmap which aren't correct should logged like Invalid GVK format, Invalid JsonPatch ops. We could have cli command to validate adapter logic.

## Design Details

### Generic Adapter via ConfigMap

The `kueue-manager-config` configmap will be expanded to configure the generic adapter.

```
     # In the multikueue Kueue Config
     multikueue:
       externalFrameworks:
         - group: "pipelinerun.tekton.dev"
           version: "v1"
           kind: "PipelineRun"
           managedBy: ".spec.managedBy"
           creationPatches:
           syncPatches:
            managedBy: ".spec.managedBy"
            creationPatches:
            # Used to define what to patch while creating in worker. // By default, we can assume replace with null for managedBy field.
                - op: replace
                  path: /spec/managedBy
                  value: null
            syncPatches:
            # Copy the entire status block from the remote object to the local one. By default, we can assume /status. Unless some other fields also need to be copied.
                - op: replace
                  path: /status
                  from: /status
```

We can have minimal configuration with default values for most of fields:
```
     # In the multikueue Kueue Config
     multikueue:
       externalFrameworks:
         - group: "pipelinerun.tekton.dev"
           version: "v1"
           kind: "PipelineRun"
```
This will translate to the former config since we will have the following default values:
```
            managedBy: ".spec.managedBy"
            creationPatches:
                - op: replace
                  path: /spec/managedBy
                  value: null
            syncPatches:
                - op: replace
                  path: /status
                  from: /status
```


For every entry in the `multiKueue.externalFrameworks` we register a MultiKueue adapter which implements the [MultiKueueAdapter interface](https://github.com/kubernetes-sigs/kueue/blob/22e4502335b0116300b3dcc63d237cf8b7ac759a/pkg/controller/jobframework/interface.go#L235C5-L235C6), in particular:
- *~IsJobManagedByKueue~*: Reads the local job object and uses the ~managedBy~ JSONPath to verify it's managed by MultiKueue.
- *~SyncJob~ (Create)*: Clones the local job object, applies the ~creationPatches~, adds the standard MultiKueue labels (~prebuilt-workload~, ~multikueue-origin~), and creates the resulting object on the worker cluster.
- *~SyncJob~ (Status Sync)*: Fetches the local and remote job objects. It then iterates through the ~statusSyncPatches~, applying each one to construct a patch for the local job's status subresource.

A significant benefit of this approach is the potential to replace the implementation of several built-in adapters (e.g., `JobSet`, `RayJob`, `RayCluster`) by registering the generic adapter for their GVKs. This would lead to a reduction in specialized code and improve maintainability.

However, the generic adapter is not a universal solution. It cannot replace adapters for workloads that have unique lifecycle management mechanisms. This is important for maintainers of external frameworks to consider when choosing an implementation path. Notable exceptions include:

- **Job**: The current adapter relies on the `MultiKueueBatchJobWithManagedBy` environment variable. This is a temporary limitation until the `JobManagedBy` feature in Kubernetes reaches General Availability.
- **Pod**: The Pod adapter uses `schedulingGates` instead of the `managedBy` field. Extending the generic adapter to support this mechanism is out of scope for this KEP.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.
- We will test the configmap parsing and validation logic.
- We will test the json patch logic.
- We will test the generic adapter.

##### Prerequisite testing updates

#### Unit Tests

Target is to keep same level or increase the coverage for all of the modified packages.
Cover the critical part of newly created code.

- `<package>`: `<date>` - `<test coverage>`

#### Integration tests

- A test will be added that creates a ~ConfigMap~ with rules for a mock GVK.
- We will then create a ~Workload~ for that mock GVK and verify that the reconciler correctly applies the creation and status patches to mock remote objects.
- Another test will verify that a ~Workload~ for an unconfigured GVK is correctly rejected.

### Graduation Criteria

#### Alpha
- **Milestone:** v0.14
- **Feature Gate:** `MultiKueueAdaptersForCustomJobs`
- Implementation is behind a feature gate.
- Support for the default placement of the `managedBy` and `status` fields (`.spec.managedBy` and `.status`) is implemented.
- Support for custom patches (for different placement of `managedBy` or `status`) is implemented.

#### Beta
- **Milestone:** v0.15
- All major reported bugs are fixed.
- The feature gate is enabled by default.
- Re-evaluate replacing the existing adapters for built-in integrations with the in-memory configuration of the generic adapter.

#### GA

- All reported bugs are fixed.
- The feature gate is removed.


## Implementation History

* 2024-06-20 First draft
* 2025-07-15 Second draft


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

4. Workload-Only Sync for External Controllers: This approach allows external controllers to retain full responsibility for their job's lifecycle, with Kueue providing the core scheduling and workload synchronization. This model offers maximum decoupling but requires the external controller author to implement their own remote job creation and status-syncing logic.

5. Using AppWrapper: Using AppWrapper was considered as an alternative. However, this approach has several drawbacks for certain loads.

Reasons to defer:
   - Job requiring status synchronization: There is no out-of-the-box way to propagate the status of the wrapped resource from the worker cluster back to the management cluster. This would require writing a custom controller to handle status synchronization.
   - Abstraction Layer: It introduces an additional layer of abstraction. Users would need to wrap their custom resources (like Tekton PipelineRun) in an AppWrapper, which complicates the user experience.
   - PodSpec Requirement: AppWrapper might not be suitable for all custom resources, especially those that do not expose a PodSet in their spec.
   - Complexity: While a controller could be built to address some of these issues, it would increase the complexity of the overall solution, particularly for CI/CD use cases.
