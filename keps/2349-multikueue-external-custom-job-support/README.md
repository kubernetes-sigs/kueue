# KEP-2349: MultiKueue external custom Job support

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories ( Optional )](#user-stories--optional-)
  - [Constraints](#constraints)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [Risks](#risks)
    - [Mitigations](#mitigations)
- [Design Details](#design-details)
  - [Generic Adapter via ConfigMap](#generic-adapter-via-configmap)
  - [Workload-Only Sync for External Controllers](#workload-only-sync-for-external-controllers)
  - [Test Plan](#test-plan)
      - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary
 This KEP introduces a mechanism to support MultiKueue for custom job frome external controllers without requiring modifications changes in external job controllers.  This introduces a generic, built-in MultiKueue adapter that is configurable via a configmap.

## Motivation

Many Kueue adopters have specific CustomResources which might not be public.
Kueue should give a possibility to manage such CustomResources across multiple clusters.

### Goals

Give a possibility for external controllers to manage private CustomResources in a MultiKueue environment.


### Non-Goals

## Proposal

MultiKueue controller will be configurable via configmap. Based on this configmap, a generic MultiKueueAdapter will be implemented within Kueue. When processing a workload without supported built-in adapter, generic MultiKueueAdapter will be called based on gvk defined in configmap. It will use configuration defined to manage lifycycle's on the worker clusters.

### User Stories ( Optional )
Ability to schedule resources not yet supported by kueue like Tekton.

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
     # In the main Kueue ConfigMap
     integrations:
       externalFrameworks:
         # OLD FORMAT
         - "taskrun.tekton.dev"
         # NEW, EXPANDED FORMAT (for MultiKueue)
         - group: "pipelinerun.tekton.dev"
           version: "v1"
           kind: "PipelineRun"
           multiKueueAdapter:
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

       The  workload reconciler will be modified so that when it encounters a workload whose owner GVK does not match any of the built-in adapters, it will:
        -  Look up the GVK from the configmap..
        - If no entry is found, the workload is rejected.
        - If an entry is found, it will use a generic adapter that performs actions based on the rules:
        - *~IsJobManagedByKueue~*: Reads the local job object and uses the ~managedBy~ JSONPath to verify it's managed by MultiKueue.
        - *~SyncJob~ (Create)*: Clones the local job object, applies the ~creationPatches~, adds the standard MultiKueue labels (~prebuilt-workload~, ~multikueue-origin~), and creates the resulting object
       on the worker cluster.
           - *~SyncJob~ (Status Sync)*: Fetches the local and remote job objects. It then iterates through the ~statusSyncPatches~, applying each one to construct a patch for the local job's status
       subresource.

### Workload-Only Sync for External Controllers

This approach allows external controllers to retain full responsibility for their job's lifecycle, with Kueue providing the core scheduling and workload synchronization.
- **External Controller Role**: The controller manages its custom job and creates a corresponding `Workload` on the management cluster. It watches for the `Workload` to be admitted by Kueue and synced to a worker cluster. Upon seeing the synced `Workload`, the controller is then responsible for creating the actual job on that worker cluster.
- **MultiKueue ACC Role**: The MultiKueue Admission Check Controller is job-agnostic. Its only role is to sync the `Workload` object to the appropriate worker cluster once it's admitted. It requires no knowledge of the custom job itself.
- **Trade-offs**: This model offers maximum decoupling but requires the external controller author to implement their own remote job creation and status-syncing logic.

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


## Implementation History

* 2024-06-20 First draft
* 2025-07-15 Second draft


## Drawbacks


## Alternatives

1. Dedicated API for MultiKueue communication between admission check controller and abstraction layer controller.

2. Don't do any code changes, a custom controller can: Instead of having MultiKueue cluster associated with controller name, change the Multikueue cluster status to use a custom active condition for old controllers (controller name) that use it.

3. Introduce ability to create Multikueue Admission Check controllers with custom set of adapters.
