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
  - [Notes/Constraints/Caveats (Optional)](#notesconstraintscaveats-optional)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
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

The enhancement aims to introduce the exposure of local queue metrics to users, providing detailed insights into workload 
processing specific to individual namespaces / tenants.

## Motivation

Metrics related to local queues are invaluable for batch users (usually with namespace-scoped permissions), as they provide 
essential visibility and historical trends about their workloads. Currently, metrics are available for ClusterQueues but 
not for namespace-scoped batch users. Cluster queue metrics are often ineffective for batch users and namespace admins 
because they are global and cannot be filtered by namespaces. Furthermore, accessing cluster-scoped metrics 
in secured Prometheus instances is generally restricted to cluster admin users with cluster level permissions across all namespaces and 
tenants. This restriction makes it challenging for batch users to obtain the specific metrics they need for effective workload 
management and to gain insights into their workloads within their limited scope of access.

### Goals

1. Introduce the API changes required to enable Local Queue metrics. 
1. List the Prometheus metrics that would be exposed for Local Queues.  

### Non-Goals

1. Discuss the implementation details on where these metrics need to be collected in codebase.

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
	
	// EnableLocalQueueResources, if true the local queue resource usage and quotas
	// metrics will be reported.
	// +optional
	EnableLocalQueueResources bool `json:"enableLocalQueueResources,omitempty"`
	
	// LocalQueueMetricOptions specifies the configuration options for local queue metrics.
	LocalQueueMetricOptions *metricsOptions `json:"localQueueMetricOptions,omitempty"`
}

// metricsOptions defines the configuration options for local queue metrics.
// If left empty, then metrics will expose for all local queues across namespaces.
type metricsOptions struct {
	// NamespaceSelector can be used to select namespaces in which the local queues should
	// report metrics.
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`
	
	// QueueSelector can be used to choose the local queues that need metrics to be collected. 
	QueueSelector *metav1.LabelSelector `json:"queueSelector,omitempty"`
}
```

To reduce cardinality, and enable selection of metrics for local queues, the following
knobs will be available:

1. If `EnableLocalQueueResources` is false, then metrics will not be exposed.  
1. If `EnableLocalQueueResources` is true, and `LocalQueueMetricOptions` is **not** provided - metrics will be exposed for all local queues.
1. If `EnableLocalQueueResources` is true and `LocalQueueMetricOptions`  is provided - metrics will be collected for local queues that match specified label selectors.

### List of metrics for Local Queues:

In the first iteration, following are the list of metrics that would contain information on Local Queue statuses:

| Metrics Name                   | Prometheus Type | Description                                                 | 
|--------------------------------|-----------------|-------------------------------------------------------------|
| local_queue_pending_workloads  | Gauge           | The number of pending workloads                             |
| local_queue_reserved_workloads | Counter         | Total number of workloads in the LocalQueue reserving quota |
| local_queue_admitted_workloads | Counter         | Total number of admitted workloads                          |
| local_queue_resource_usage     | Gauge           | Total quantity of used quota per resource for a Local Queue |

Each of these metrics will be augmented with relevant Prometheus labels, indicating the local queue name, namespace, 
and any other unique identifiers as required during implementation.

### Test Plan

[X] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Unit Tests

There are existing unit tests for prometheus metrics: https://github.com/kubernetes-sigs/kueue/blob/main/pkg/metrics/metrics_test.go.
However, unit tests to ensure coverage for any additional local queue metrics will be added. 

- `<package>`: `<date>` - `<test coverage>`

#### Integration tests

The integration will address the following scenarios:

1. Metrics for local queues are accurately reported throughout the lifecycle of workloads in local queues.
1. Metrics are removed when a local queue is deleted from the cache.

### Graduation Criteria

## Implementation History

## Drawbacks

If not implemented correctly, in certain scenarios enabling local queue metrics for all namespaces across all local queues can lead to issues 
with cardinality and system overload. To mitigate this, configuration options are provided to selectively enable metrics 
reporting for specific local queues.
