---
title: "Overview"
linkTitle: "Overview"
weight: 1
description: >
  Why Kueue?
---

Kueue is a kubernetes-native system that manages quotas and how jobs consume them. Kueue decides when a job should wait, when a job should be admitted to start (as in pods can be created) and when a job should be preempted (as in active pods should be deleted).

## Why use Kueue

You can install Kueue on top of a vanilla Kubernetes cluster. Kueue does not replace any existing Kubernetes components. Kueue is compatible with cloud environments where:

* Compute resources are elastic and can be scaled up and down.
* Compute resources are heterogeneous (in architecture, availability, price, etc.).

Kueue APIs allow you to express:

* Quotas and policies for Fair Sharing among tenants.
* Resource fungibility: if a resource flavor is fully utilized, Kueue can admit the job using a different flavor.

A core design principle for Kueue is to avoid duplicating mature functionality in Kubernetes components and well-established third-party controllers. Autoscaling, pod-to-node scheduling and job lifecycle management are the responsibility of cluster-autoscaler, kube-scheduler and kube-controller-manager, respectively. Advanced admission control can be delegated to controllers such as gatekeeper.

## Features overview

- **Job management:** Support job queueing based on [priorities](/docs/concepts/workload/#priority) with different [strategies](/docs/concepts/cluster_queue/#queueing-strategy): `StrictFIFO` and `BestEffortFIFO`.
- **Advanced Resource management:** Comprising: [resource flavor fungibility](/docs/concepts/cluster_queue/#flavorfungibility), [Fair Sharing](/docs/concepts/preemption/#fair-sharing), [Cohorts](/docs/concepts/cohort) and [preemption](/docs/concepts/cluster_queue/#preemption) with a variety of policies between different tenants.
- **Integrations:** Built-in support for popular jobs, e.g. [BatchJob](/docs/tasks/run/jobs/), [Kubeflow training jobs](/docs/tasks/run/kubeflow/), [RayJob](/docs/tasks/run/rayjobs/), [RayCluster](/docs/tasks/run/rayclusters/), [JobSet](/docs/tasks/run/jobsets/),  [AppWrappers](/docs/tasks/run/appwrappers/), [plain Pod and Pod Groups](/docs/tasks/run/plain_pods/).
- **System insight:** Build-in [prometheus metrics](/docs/reference/metrics/) to help monitor the state of the system, and on-demand visibility endpoint for [monitoring of pending workloads](/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/).
- **AdmissionChecks:** A mechanism for internal or external components to influence whether a workload can be [admitted](/docs/concepts/admission_check/).
- **Advanced autoscaling support:** Integration with cluster-autoscaler's [provisioningRequest](/docs/concepts/admission_check/provisioning_request/#job-using-a-provisioningrequest) via admissionChecks.
- **All-or-nothing with ready Pods:** A timeout-based implementation of [All-or-nothing scheduling](/docs/tasks/manage/setup_wait_for_pods_ready/).
- **Partial admission and dynamic reclaim:** mechanisms to run a job with [reduced parallelism](/docs/tasks/run/jobs/#partial-admission), based on available quota, and to [release](/docs/concepts/workload/#dynamic-reclaim) the quota the pods complete..
- **Mixing training and inference**: Simultaneous management of batch workloads along with serving workloads (such as [Deployments](/docs/tasks/run/deployment/) or [StatefulSets](/docs/tasks/run/statefulset/))
- **Multi-cluster job dispatching:** called [MultiKueue](/docs/concepts/multikueue/), allows to search for capacity and off-load the main cluster.
- **Topology-Aware Scheduling**: Allows to optimize the pod-pod communication throughput by [scheduling aware of the data-center topology](/docs/concepts/topology_aware_scheduling/).
- **Topology Spreading**: Allows to improve availability of serving workloads by [spreading separate workloads across failure domains](/docs/concepts/topology_aware_scheduling/#topology-spreading), limiting how many of them may occupy a single zone or rack.
- **Dynamic Resource Allocation (DRA):** Quota management for devices such as GPUs requested through [DRA](/docs/concepts/dynamic_resource_allocation/), either with ResourceClaimTemplates or extended resources, including partitionable devices. Consumable capacity is available as an alpha feature.

## In-tree integrations

Kueue provides the following in-tree integrations:

| Name | Graduation level |
|------|------------------|
| [AppWrapper](/docs/tasks/run/appwrappers/) | Stable |
| [Batch Job](/docs/tasks/run/jobs/) | Stable |
| [Deployment](/docs/tasks/run/deployment/) | Stable |
| [JobSet](/docs/tasks/run/jobsets/) | Stable |
| [LeaderWorkerSet](/docs/tasks/run/leaderworkerset/) | Stable |
| [MPIJob](/docs/tasks/run/kubeflow/mpijobs/) | Stable |
| [Pod and Pod groups](/docs/tasks/run/plain_pods/) | Stable |
| [RayCluster](/docs/tasks/run/rayclusters/) | Stable |
| [RayJob](/docs/tasks/run/rayjobs/) | Stable |
| [RayService](/docs/tasks/run/rayservices/) | Stable |
| [SparkApplication](/docs/tasks/run/kubeflow/sparkapplications/) | Alpha |
| [StatefulSet](/docs/tasks/run/statefulset/) | Stable |
| [TrainJob (Kubeflow Trainer v2)](/docs/tasks/run/trainjobs/) | Alpha |
| [JAXJob (Kubeflow Trainer v1)](/docs/tasks/run/kubeflow/jaxjobs/) | Deprecated |
| [PaddleJob (Kubeflow Trainer v1)](/docs/tasks/run/kubeflow/paddlejobs/) | Deprecated |
| [PyTorchJob (Kubeflow Trainer v1)](/docs/tasks/run/kubeflow/pytorchjobs/) | Deprecated |
| [TFJob (Kubeflow Trainer v1)](/docs/tasks/run/kubeflow/tfjobs/) | Deprecated |
| [XGBoostJob (Kubeflow Trainer v1)](/docs/tasks/run/kubeflow/xgboostjobs/) | Deprecated |

For the deprecated Kubeflow Trainer v1 integrations, migrate to
[TrainJob (Kubeflow Trainer v2)](/docs/tasks/run/trainjobs/) or
[JobSet](/docs/tasks/run/jobsets/).

## Job-integrated features

| Feature                                                                                                         | Batch&nbsp;Job | JobSet | PaddleJob | PytorchJob | TFJob | TrainJob | XGBoostJob | MPIJob | JAXJob | Pod | RayCluster | RayJob | AppWrapper | Deployment | StatefulSet | LeaderWorkerSet |
|-----------------------------------------------------------------------------------------------------------------|----------------|--------|-----------|------------|-------|----------|------------|--------|:------:|-----|------------|--------|------------|------------|-------------|-----------------|
| [Dynamic Reclaim](/docs/concepts/workload/#dynamic-reclaim)                                                     | +              | +      |           |            |       | +        |            |        |        | +   |            |        |            |            |             |                 |
| [MultiKueue](/docs/concepts/multikueue/)                                                                        | +              | +      | +         | +          | +     | +        | +          | +      |   +    |     | +          | +      | +          |            | +           | +               |
| [PartialAdmission](/docs/tasks/run/jobs/#partial-admission)                                                     | +              |        |           |            |       |          |            |        |        |     |            |        |            |            |             |                 |
| [Workload Priority Class](/docs/concepts/workload_priority_class/)                                              | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [FlavorFungibility](/docs/concepts/cluster_queue/#flavorfungibility)                                            | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [ProvisioningACC](/docs/concepts/admission_check/provisioning_request/)                                         | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [VisibilityOnDemand](/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/)                 | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [PrioritySortingWithinCohort](/docs/concepts/cluster_queue/#flavors-and-borrowing-semantics)                    | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [LendingLimit](/docs/concepts/cluster_queue/#lendinglimit)                                                      | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [All-or-nothing with ready Pods](/docs/concepts/workload/#all-or-nothing-semantics-for-job-resource-assignment) | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [Fair Sharing](/docs/concepts/preemption/#fair-sharing)                                                         | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [Topology Aware Scheduling](/docs/concepts/topology_aware_scheduling)                                           | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [Topology Spreading](/docs/concepts/topology_aware_scheduling/#topology-spreading)                              |                |        |           |            |       |          |            |        |        | +   |            |        |            | +          |             | +               |

## High-level Kueue operation

![High Level Kueue Operation](/images/theory-of-operation.svg)

To learn more about Kueue concepts, see the [concepts](/docs/concepts) section.

To learn about different Kueue personas and what you can do with Kueue, see the [tasks](/docs/tasks) section.
