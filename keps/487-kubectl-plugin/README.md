# KEP-487: Kubectl plugin for listing objects

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
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Version](#version)
  - [Describe](#describe)
  - [List](#list)
  - [Stop](#stop)
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

Provide an easy way to view and manage kueue abstractions via command line tool.

## Motivation

Create tools that provide useful information in an intuitive way for cluster users and could be later extended as managing tool for cluster administrators.
As a cluster user I don’t need to dive into the difference between job and Workload concept in kueue and should be able quickly discover the job/Workload and ClusterQueue status or see the error message what is misconfigured/should be fixed.

### Goals

A successful plugin should be able:

- Display the Workload status.
- Describe Workload.
- List Workloads in specific queue/namespace.
- List admitted/pending workloads
- List all ClusterQueues.
- Describe ClusterQueue.
- Stop Workload.
- Stop ClusterQueue.

### Non-Goals

- Create or modify Workloads, cluster queues.
- Streaming logs.

## Proposal

We propose creating a Kubectl plugin that exposes kueue specific information. The main command-line interactions will take the following shape:

```bash
$kubectl kueue <type> <command> <name> <flags> <options>
```

Set of supported commands: `list`, `describe`, `stop`.
Set of supported types: `workload`, `clusterqueue`, `localqueue`.
Sets of supported flags are different for each command/type.

The plugin could be downloaded directly from the repo or installed by the Krew plugging manager.

### User Stories (Optional)

#### Story 1

As a user submitting workloads, I want to easily see the status of an entire Workload, or check on why my workload is not being admitted. I can do this with the new proposed plugin.

```bash
# List workloads in the cluster.
$kubectl kueue workloads get
# Describe particular workload.
$kubeclt kueue workload describe <name>
# Display state and conditions to see what could be wrong with workload.
$kubeclt kueue workload describe <name> --status

```

#### Story 2

As a cluster admin I want to have an easy way to manage cluster queues:

```bash
$kubectl kueue clusterqueue stop <queue-name>
```

### Risks and Mitigations

The API of Workload and ClusterQueue doesn't have a State field. Here we are making assumptions regarding the state of the object based on the values of other fields.
We need to make sure to have good test coverage in order to quickly observe any API changes that may affect the state of the object. In case of any change the state field should be disabled.

## Design Details

As a kubectl plugin, the name should have `kubectl-` prefix. The most obvious plugin naming choice would be `kubectl-kueue`.
The code will live in cmd/ directory.

The CLI command will have the following format:

```bash
$kubectl kueue <type> <command> <name> <flags> <options>
```

### Version

```bash
$kubectl kueue version
kueue pod was not found in kueue-system namespace

$kubectl kueue version
kueue:v0.5.0
```

`kubectl kueue version` command will verify that kueue is running by checking the `kueue-controller-manager` pod in kueue-system namespace and return the version of kueue based on the image name in pod spec.

### Describe

```bash
$kubectl kueue workload describe job-team-a --namespace=team-a --status
State: Pending
Status
  Conditions:
    Last Transition Time:  2023-11-09T09:26:58Z
    Message:               couldn't assign flavors to pod set main: resource ephemeral-storage unavailable in ClusterQueue
    Reason:                Pending
    Status:                False
    Type:                  QuotaReserved

$kubectl kueue workload describe job-team-a --namespace team-a --spec
Spec:
  Pod Sets:
    Count:  3
    Name:   main
    Template:
      Metadata:
        Labels:
          batch.kubernetes.io/controller-uid:  37a03eb0-5c59-41f2-be3c-e9e5ca75bc7c
          batch.kubernetes.io/job-name:        sample-job-team-a-vdjg9
          Controller - UID:                    37a03eb0-5c59-41f2-be3c-e9e5ca75bc7c
          Job - Name:                          sample-job-team-a-vdjg9
      Spec:
        Containers:
          Args:
            10s
          Image:              gcr.io/k8s-staging-perf-tests/sleep:latest
          Image Pull Policy:  Always
          Name:               dummy-job
          Resources:
            Limits:
              Cpu:                  500m
              Ephemeral - Storage:  512Mi
              Memory:               512Mi
              nvidia.com/gpu:       1
            Requests:
              Cpu:                     500m
              Ephemeral - Storage:     512Mi
              Memory:                  512Mi
              nvidia.com/gpu:          1
          Termination Message Path:    /dev/termination-log
          Termination Message Policy:  File
        Dns Policy:                    ClusterFirst
        Node Selector:
          cloud.google.com/gke-accelerator:  nvidia-tesla-t4
        Restart Policy:                      Never
        Scheduler Name:                      default-scheduler
        Security Context:
        Termination Grace Period Seconds:  30
  Priority:                                0
  Priority Class Source:                   
  Queue Name:                              lq-team-a

```

In order to make kueue CLI simple, we are going to have only one abstraction for jobs specific information — workload. Currently the `describe` command will mostly duplicate the `kubectl describe` command with few modifications:
`--status` option will display Workload.Status field and state of Workload.
There is no `State` field in Workload, but it could be identified from other fields. The `Workload.Status` has an `Admitted` field, in case of pending workload it is empty, so from the user point of view it's not clear what the state of the Workload is. I suggest adding the state information that displays `Admitted` if the `Status.Admitted` is not empty and `Pending` if the `Status.Admitted` is empty.
The `--spec` option will display `Workload.Spec` information.

For ClusterQueue the Status field might look confusing. For example for misconfigured ClusterQueur `Status.Conditions[-1].Type` have value `Active` and `Status.Conditions[-1].Status` have value `False`. I suggest including a state field that will display `Error` if `Status.Conditions.Status` is False and `Active` if `Status.Conditions.Status` is `True` and `Status.Conditions.Type` is `Active`. Later, when we'll support other types, the logic will probably stay the same, but it should be double checked.

### List

```bash
$kubectl kueue worlkoad list --clusterqueue=cq
| Namespace | Name     |  Queue  | ClusterQueue | Pods |   State   | Age
|-----------|----------|---------|--------------|------|-----------|-----
| default   | wl-1     | queue-1 |   cq         |   2  | Admitted  | 2h
| default   | wl-2     | queue-2 |   cq         |   5  | Pending   | 2h


$kubectl kueue clusterqueue list
| Name             | Cohort   |  State  | PENDING WORKLOADS
|------------------|----------|---------|-------------------
| cluster-queue-1  | cohort   | Active  | 2  
| cluster-queue-2  |          | Error   | 0    

```

The list command will use kubectl get command with CustumnColumntPrinter and with attached column `State`, that was described earlier. For Workloads we'll use sorting for pending workloads that are on the same ClusterQueue based on their position on the queue. Since ClusterQueue is not namespaced object,
I suggest using all namespaces by default. The command will support --state, --cohort, --clusterqueue, --namespace flags when applicable.

### Stop

```bash
$kubectl kueue worlkoad stop workload-name --clusterqueue cluster-queue-name
...Done

$kubectl kueue clusterqueue stop cluster-queue-name
...Done
```

At the moment this fuctionality is not supported and will be added later.

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

- `<package>`: `<date>` - `<test coverage>`

#### Integration tests

<!--
Describe what tests will be added to ensure proper quality of the enhancement.

After the implementation PR is merged, add the names of the tests here.
-->

### Graduation Criteria

<!--

Clearly define what it means for the feature to be implemented and
considered stable.

If the feature you are introducing has high complexity, consider adding graduation
milestones with these graduation criteria:
- [Maturity levels (`alpha`, `beta`, `stable`)][maturity-levels]
- [Feature gate][feature gate] lifecycle
- [Deprecation policy][deprecation-policy]

[feature gate]: https://git.k8s.io/community/contributors/devel/sig-architecture/feature-gates.md
[maturity-levels]: https://git.k8s.io/community/contributors/devel/sig-architecture/api_changes.md#alpha-beta-and-stable-versions
[deprecation-policy]: https://kubernetes.io/docs/reference/using-api/deprecation-policy/
-->

## Implementation History

<!--
Major milestones in the lifecycle of a KEP should be tracked in this section.
Major milestones might include:
- the `Summary` and `Motivation` sections being merged, signaling SIG acceptance
- the `Proposal` section being merged, signaling agreement on a proposed design
- the date implementation started
- the first Kubernetes release where an initial version of the KEP was available
- the version of Kubernetes where the KEP graduated to general availability
- when the KEP was retired or superseded
-->

## Drawbacks

<!--
Why should this KEP _not_ be implemented?
-->

## Alternatives

<!--
What other approaches did you consider, and why did you rule them out? These do
not need to be as detailed as the proposal, but should include enough
information to express the idea and why it was not acceptable.
-->
