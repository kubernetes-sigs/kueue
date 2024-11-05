# KEP-168-2: Pending-workloads-visibility

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
  - [Cons of the current solution](#cons-of-the-current-solution)
    - [Size of the queue](#size-of-the-queue)
    - [Consistency across all LocalQueues](#consistency-across-all-localqueues)
    - [Expanding API in the future](#expanding-api-in-the-future)
    - [Delay](#delay)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [DDoS](#ddos)
    - [Payload size](#payload-size)
- [Design Details](#design-details)
  - [API Details](#api-details)
  - [API endpoints:](#api-endpoints)
    - [List pending workloads in ClusterQueue](#list-pending-workloads-in-clusterqueue)
    - [List pending workloads in LocalQueue](#list-pending-workloads-in-localqueue)
  - [API Objects:](#api-objects)
  - [Future extensions](#future-extensions)
  - [Test Plan](#test-plan)
    - [Overview](#overview)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [E2E tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [GA](#ga)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
  - [Alternative approaches](#alternative-approaches)
    - [Approach described in <a href="https://github.com/kubernetes-sigs/kueue/tree/main/keps/168-pending-workloads-visibility">KEP#168</a>](#approach-described-in-kep168)
    - [Extend API using CRDs](#extend-api-using-crds)
  - [Alternatives within the proposal](#alternatives-within-the-proposal)
    - [apiserver-builder library](#apiserver-builder-library)
<!-- /toc -->

## Summary

This KEP proposes to introduce a new API that allows users to on-demand fetch information about pending workloads in both ClusterQueue and LocalQueue. Users will be able to look up the position of a specific workload in both types of queues and list pending workloads in a specific queue.

## Motivation

As presented in [KEP#168](https://github.com/kubernetes-sigs/kueue/tree/main/keps/168-pending-workloads-visibility), there is currently a proposal for a mechanism that supports fetching the order of pending workloads, but it comes with a lot of cons. This proposal addresses all of those problems.

### Cons of the current solution

#### Size of the queue

There are a few scalability concerns. The first one is that the number of fetched pending workloads is limited by the etcd object's size limit. By default, a user is able to fetch only 10 workloads stored at the head of a queue. This number can be increased up to 4000, but comes with a performance loss.

#### Consistency across all LocalQueues

Another scalability drawback is that in a Kueue setup with a lot of LocalQueues it is very likely to hit the Kueue QPS. Assuming Kueue setup with multiple LocalQueues pointing to the same ClusterQueue, Kueue needs to send updates to all LocalQueues to update their status, in order to keep the workload positional information up-to-date. This consumes `QPS` which can lead to blocking other requests. Although we can use a client separate from a default one, it would not not completely resolve all scalability issues.

#### Expanding API in the future

Moreover, there are some functional issues with the current approach. It does not expose any information about pending workloads except for ```name```, ```namespace```, and position in a queue (by listing workloads in order). Adding new fields would result in a decrease in the potential maximum number of fetched pending workloads, caused by the etcd object's size.

#### Delay

Additionally, in the previous proposal, Kueue updated the most prioritized workloads every 5 seconds. It is configurable, but since computing the most prioritized workloads can be expensive, it cannot be significantly reduced.
Users can observe outdated information, which might not be convenient.


### Goals

- Support listing pending workloads on positions from X to Y in a ClusterQueue, no matter the size of the queue, and without delay,
- Support listing pending workloads on positions from X to Y in a LocalQueue, no matter the size of the queue, and without delay,
- Provide consistent data across all the LocalQueues without hitting `QPS`.

### Non-Goals

- Provide ETA (Estimated Time of Arrival) for a workload,
- Provide information on whether a workload is admissible,
- Provide information about the requested resource for a workload,

## Proposal
Add new API exposing information about pending workloads relevant for their position in the queue, along with the position itself. There are two such endpoints:
1. List the pending workloads in ClusterQueue,
2. List the pending workloads in LocalQueue,

In order to expose the API endpoints we introduce a new Extension API server.

### User Stories

#### Story 1 

As a user of Kueue with LocalQueue visibility only, I would like to know the position in the ClusterQueue of a workload that I've just submitted, no matter how big the queue is. Knowing the position and assuming stable velocity in the ClusterQueue, would allow me to estimate the arrival time of my workload.

Provided by the [LocalQueue endpoint](#list-all-pending-workloads-in-localqueue).

#### Story 2 

As an administrator of Kueue with ClusterQueue visibility, I would like to be able to check directly and compare the positions of pending workloads in the queue, no matter the size of it. It is important that data across all LocalQueue is consistent, and no two workloads have the same position in ClusterQueue. This will help me answer users' questions about their workloads.

Provided by the [ClusterQueue endpoint](#list-all-pending-workloads-in-clusterqueue).

#### Story 3 

As a developer who uses Kueue, I would like to be able to monitor the state of my ClusterQueue/LocalQueue using dashboards. I need a mechanism that allows me to easily build it.

Provided by the [ClusterQueue endpoint](#list-all-pending-workloads-in-clusterqueue) and the [LocalQueue endpoint](#list-all-pending-workloads-in-localqueue).


### Risks and Mitigations

#### DDoS

One risk we foresee is that the server may be exposed to DDoS attacks. A potential attacker may flood the server with requests, which will result in constantly locking the Kueue Manager. To mitigate this risk, we plan on relying on throttling, so that even with numerous requests, the Kueue Manager remains functional. The first approach we propose is to use [API server P&F mechanism](https://kubernetes.io/docs/concepts/cluster-administration/flow-control/). Additionally, based on the user feedback, we may consider another caching mechanism inside Kueue.

#### Payload size

Another risk we took into account is that the payload size would be too large in the case of 100k pending workloads. However, in the worst case scenario (which we foresee as a rather unrealistic one, since it would mean all string fields would be filled with 256 chars) its size is about 1,4kB. Even with 100k pending workloads, it takes 140 MB, which is still a reasonable number compared to `metrics-server's` payloads. Hence, we believe it should not be a concern. This is also mitigated by the [query parameters](#api-details) introduced below.

## Design Details

The proposal introduces a new server running on the Kueue's pod. It computes the current state of KueueManager without any additional request overhead. The server uses the [K8s API Aggregation Layer](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/apiserver-aggregation/) mechanism. The same mechanism is used by the [metrics-server](https://github.com/kubernetes-sigs/metrics-server). There will be no additional etcd objects or need to use existing ones. No additional requests to sync information across LocalQueues will be required. 

Similarly to the ```metrics-server``` the server will be implemented with the [apiserver library](https://github.com/kubernetes/apiserver), which provides authentication and authorization.

All computation will be done on-demand without additional reconcile loops.

The server provides that:
- Pending workloads are returned according to their actual status without significant delay. This includes adding and removing/admitting new workloads with various priorities,
- Adding workloads to one LocalQueues results in position changes for workloads submitted to other LocalQueues,
- Data is consistent across all LocalQueues,
- User with only LocalQueue visibility cannot access the list of pending workloads for ClusterQueue.

### API Details

We introduce a new API that will extend the existing one.

There will be separate endpoints exposing the information about pending workloads for LocalQueues, and ClusterQueues. Each endpoint exposes information about a pending workload, such as:
- workload's position in a ClusterQueue,
- workload's position in a LocalQueue,
- workload's priority,
- creation timestamp.

The API does not allow for the modification of any objects.

Regular users will have access only to the LocalQueues they are assigned to. However, they will be able to fetch information about the global position of a workload in a ClusterQueue, without any details about workloads in different LocalQueues. 

Administrators will have access to all the data at the ClusterQueue level. They will be able to view all the workloads, no matter the LocalQueues the workloads are assigned to.

The API also allows user to fetch information about part of the Cluster/LocalQueue from position X to Y. There are two query parameters to do so:
- `offset` indicates position of the first fetched workload - default: `0`
- `limit` indicates max number of workloads to be fetched - default: `1000`

Thanks to these parameters our server also support pagination.

### API endpoints:

We introduce a new API group ```visibility.kueue.x-k8s.io``` that aggregates following endpoints: 

#### List pending workloads in ClusterQueue

```
GET /apis/visibility.kueue.x-k8s.io/VERSION/clusterqueues/CQ_NAME/pendingworkloads?offset=0&limit=1000
```

#### List pending workloads in LocalQueue
```
GET /apis/visibility.kueue.x-k8s.io/VERSION/namespaces/LQ_NAMESPACE/localqueues/LQ_NAME/pendingworkloads?offset=0&limit=1000
```

Those endpoints can be accessed using `kubectl get --raw <ENDPOINT_PATH>` command.

Another way to access API is to use a client generated with `k8s.io/code-generator`, similarly to the core Kueue API.

### API Objects:

```
// PendingWorkload is a user-facing representation of a pending workload in both LocalQueues and ClusterQueue that summarizes necessary information from the admission order perspective
type PendingWorkload struct {
	TypeMeta TypeMeta
	ObjectMeta ObjectMeta

	LocalQueueName string
	PositionInClusterQueue int32
	PositionInLocalQueue int32
	Priority int32
}

// PendingWorkloadSummary contains a list of pending workloads in the context
// of the query (within LocalQueue or ClusterQueue).
type PendingWorkloadsSummary struct {
	TypeMeta TypeMeta
	ListMeta ListMeta

	Items []PendingWorkload
}
```

A user can easily identify the Job that is an owner of the pending workload. To enable it, the API uses `metav1.OwnerReferences` field to indicate the owner, typically the job created by the user. 

### Future extensions

The introduced API uses mechanism of subresources. It means, that in the future it can be easily extended by adding additional endpoints related e.g. to admitted workloads. Potentially the endpoint could look like this:

```
GET /apis/visibility.kueue.x-k8s.io/VERSION/clusterqueues/CQ_NAME/admitted_workloads?offset=0&limit=1000
```

### Test Plan

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Overview

Our main focus is integration tests, as most of the added code is responsible for integrating with the Kueue and RBAC roles.

#### Unit Tests

We plan on adding unit tests that cover getting a list of pending workloads at the KueueManager level.

- `pkg/visibility`: `30 Oct 2023` - `0%`

#### Integration tests

Integration tests should check if our server work correctly according to the assumptions we mentioned:
- Pending workloads are returned according to their actual status without delay.
- Adding workloads to one LocalQueues results in position changes for workloads submitted to other LocalQueues,
- Data is consistent across all LocalQueues
- User with only LocalQueue visibility cannot access the list of pending workloads for ClusterQueue

#### E2E tests

We plan on adding sanity e2e tests, and RBAC e2e tests. The e2e RBAC tests should cover scenarios:
- clusters queues can only be accessed by admin users
- local queues can be accessed by only users with the visibility to the corresponding namespaces

### Graduation Criteria

#### Alpha
- Release the new API in alpha. This allows us to adjust the API according to users' and reviewers' feedback,
- Release it with a feature gate.

#### Beta
First iteration (0.9):
- Release the API in beta and guarantee backwards compatibility,
- Merge the visibility-api.yaml into the main manifests.yaml,
- Create separate manifests for both FlowSchema and PriorityLevelConfiguration in versions v1 and v1beta3.

Second iteration (0.10):
- Merge the FlowSchema and PriorityLevelConfiguration v1 manifests into the installation manifests once the latest Kubernetes version (1.28) no longer supports v1,
- Drop the alpha API version.

#### GA
- Reconsider introducing a throttling mechanism.

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

## Alternatives

### Alternative approaches

#### Approach described in [KEP#168](https://github.com/kubernetes-sigs/kueue/tree/main/keps/168-pending-workloads-visibility) 

Use the status fields of ClusterQueues and LocalQueues

**Pros:**
- Partially already implemented

**Cons:**
- Described above and in the KEP

#### Extend API using CRDs

Extract information about the order of pending workloads to a separate CRD object.

**Pros:**
- Easy to set up

**Cons:**
- Does not address scalability concerns
- Does not provide the position in the queue for an arbitrary workload, if it's not at the head of the Kueue

### Alternatives within the proposal

#### apiserver-builder library

There is an alternative library to the [apiserver library](https://github.com/kubernetes/apiserver) called [apiserver-builder](https://github.com/kubernetes-sigs/apiserver-builder-alpha). It seemed promising as it could potentially speed up the development. However, after researching this library we had concerns about its maintenance. The old dependencies, no recent commits or pull requests indicated that this project might be abandoned. We have contacted the last maintainer of this project and confirmed that there is no planned effort into maintaining it. He also confirmed our concerns, that due to old dependencies there might be some compatibility issues if we wanted to use the latest k8s release.

**Pros:**
- Faster development

**Cons:**
- Library is not maintained
- Possible compatibility issues due to old dependencies