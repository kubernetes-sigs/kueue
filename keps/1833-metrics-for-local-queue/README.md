# KEP-1833: Enable Prometheus Metrics for Local Queues

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories (Optional)](#user-stories-optional)
    - [Story 1](#story-1)
    - [Story 2](#story-2)
    - [Story 3](#story-3)
- [Design Details](#design-details)
  - [API changes:](#api-changes)
  - [List of metrics for Local Queues:](#list-of-metrics-for-local-queues)
  - [Test Plan](#test-plan)
    - [Unit Tests](#unit-tests)
    - [Integration tests](#integration-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
<!-- /toc -->

## Summary

The enhancement aims to introduce the exposure of local queue metrics to users, providing detailed insights into workload 
processing specific to individual namespaces / tenants.

## Motivation

Metrics related to local queues are invaluable for batch users, providing essential visibility and historical trends 
about their workloads. Currently, while metrics are available for only ClusterQueues, they do not provide batch users with 
the necessary insights into their specific workloads.

### Goals

1. Introduce the API changes required to enable Local Queue metrics. 
2. List the Prometheus metrics that would be exposed for Local Queues.  

### Non-Goals

1. Discuss the implementation details on where these metrics need to be collected in codebase.
2. Discuss on metric visibility and RBAC required to enable the metrics securely for namespace admins.  

## Proposal

The proposal extends to enable collection of metrics for local queues that would be useful
for batch users and cluster administrators. 

### User Stories (Optional)

#### Story 1

As a batch user of Kueue, I want to access metrics for local queues running workloads restricted to my namespace so that 
I can monitor and analyze the performance and trends of my workloads.

#### Story 2

As an administrator of Kueue, I want to enable batch users specific to a namespace to collect metrics for their workloads 
within their namespace so that they can have visibility and insights into their own workload metrics.

#### Story 3

As an administrator of Kueue, I want to filter and gain insights on fine-grained metrics relevant to a local queue by 
namespace for specific tenants so that I can effectively manage and optimize resource usage and performance for different tenants.

## Design Details

### API changes:

The [Configuration API](https://github.com/kubernetes-sigs/kueue/blob/7ec127b05c8a0c8268e623de61914472dc5bff29/apis/config/v1beta1/configuration_types.go#L30)
currently provides the ability to enable collection of metrics for cluster queues. This API will be extended to include options for enabling metrics 
collection for local queues.

The `ControllerMetrics` that contain the option to configure metrics, will be extended as follows:

```go
type ControllerManager struct {
	...

	// Metrics contains the controller metrics configuration
	// +optional
	Metrics ControllerMetrics `json:"metrics,omitempty"`
	...
}

// ControllerMetrics defines the metrics configs.
type ControllerMetrics struct {
	...
	
	// LocalQueueMetrics is a configuration that provides enabling LocalQueue metrics and its options.
	// +optional
	LocalQueueMetrics *LocalQueueMetrics `json:"localQueueMetrics,omitempty"`
}

// LocalQueueMetrics defines the configuration options for local queue metrics.
// If left empty, then metrics will expose for all local queues across namespaces.
type LocalQueueMetrics struct {
	// Enable is a knob to allow metrics to be exposed for local queues. Defaults to false.
	Enable bool `json:"enable,omitempty`
	
	// NamespaceSelector can be used to select namespaces in which the local queues should
	// report metrics.
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`
	
	// LocalQueueSelector can be used to choose the local queues that need metrics to be collected. 
	LocalQueueSelector *metav1.LabelSelector `json:"localQueueSelector,omitempty"`
}
```

To reduce cardinality, and enable selection of metrics for local queues, the following
knobs will be available for `LocalQueueMetrics`:

| `Enable` | `NamespaceSelector` | `LocalQueueSelector` | Description                                                                                                        |
|----------|---------------------|----------------------|--------------------------------------------------------------------------------------------------------------------|
| False    | -                   | -                    | Metrics will not be exposed.                                                                                       |
| True     | -                   | -                    | Metrics for all local queues will be exposed.                                                                      |
| True     | Specified           | -                    | All LocalQueues in the specific namespaces that match the selector have metrics enabled.                           |
| True     | -                   | Specified            | All LocalQueues matching the label selector have metrics enabled.                                                  |
| True     | Specified           | Specified            | Both the selectors are applied to local queues (logical AND) to filter the ones whose metrics have to be enabled.  |
| False    | Specified           | Specified            | The selectors are disregarded, metrics will not be exposed.                                                        |

### List of metrics for Local Queues:

In the first iteration, following are the list of metrics that would contain information on Local Queue statuses:

| Metrics Name                                   | Prometheus Type | Description                                                                                         | 
|------------------------------------------------|-----------------|-----------------------------------------------------------------------------------------------------|
| local_queue_pending_workloads                  | Gauge           | The number of pending workloads.                                                                    |
| local_queue_reserved_workloads_total           | Counter         | Total number of workloads in the LocalQueue reserving quota.                                        | 
| local_queue_admitted_workloads_total           | Counter         | Total number of admitted workloads.                                                                 |
| local_queue_resource_usage                     | Gauge           | Total quantity of used quota per resource for a Local Queue.                                        |
| local_queue_evicted_workloads_total            | Counter         | The total number of evicted workloads in Local Queue.                                               |
| local_queue_reserved_wait_time_seconds         | Histogram       | The time between a workload was created or re-queued until it got quota reservation in local queue. |
| local_queue_admission_checks_wait_time_seconds | Histogram       | The time from when a workload got the quota reservation until admission in local queue.             |
| local_queue_admission_wait_time_seconds        | Histogram       | The time between a workload was created or re-queued until admission.                               |
| local_queue_status                             | Gauge           | Reports the status of the ClusterQueue.                                                             |

Each of these metrics will be augmented with relevant Prometheus labels, indicating the local queue name, namespace, 
and any other unique identifiers as required during implementation. They will be exported in the controller namespace, 
alongside cluster queue metrics, at the same endpoint.

### Test Plan

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

There are existing unit tests for prometheus metrics: https://github.com/kubernetes-sigs/kueue/blob/main/pkg/metrics/metrics_test.go.
However, unit tests to ensure coverage for any additional local queue metrics will be added.

- `pkg/metrics/`: `2024-07-19` - `48.2%`

#### Integration tests

The integration will address the following scenarios:

1. Metrics for local queues are accurately reported throughout the lifecycle of workloads in local queues.
2. Metrics are removed when a local queue is deleted from the cache.

### Graduation Criteria

## Implementation History

## Drawbacks

If not implemented correctly, in certain scenarios enabling local queue metrics for all namespaces across all local queues can lead to issues 
with cardinality and system overload. To mitigate this, configuration options are provided to selectively enable metrics 
reporting for specific local queues.
