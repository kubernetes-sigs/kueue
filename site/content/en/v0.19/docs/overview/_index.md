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

- **Job management:** Support job queueing based on [priorities](/v0.19/docs/concepts/workload/#priority) with different [strategies](/v0.19/docs/concepts/cluster_queue/#queueing-strategy): `StrictFIFO` and `BestEffortFIFO`.
- **Advanced Resource management:** Comprising: [resource flavor fungibility](/v0.19/docs/concepts/cluster_queue/#flavorfungibility), [Fair Sharing](/v0.19/docs/concepts/preemption/#fair-sharing), [Cohorts](/v0.19/docs/concepts/cohort) and [preemption](/v0.19/docs/concepts/cluster_queue/#preemption) with a variety of policies between different tenants.
- **Integrations:** Built-in support for popular jobs, e.g. [BatchJob](/v0.19/docs/tasks/run/jobs/), [Kubeflow training jobs](/v0.19/docs/tasks/run/kubeflow/), [RayJob](/v0.19/docs/tasks/run/rayjobs/), [RayCluster](/v0.19/docs/tasks/run/rayclusters/), [JobSet](/v0.19/docs/tasks/run/jobsets/),  [AppWrappers](/v0.19/docs/tasks/run/appwrappers/), [plain Pod and Pod Groups](/v0.19/docs/tasks/run/plain_pods/).
- **System insight:** Build-in [prometheus metrics](/v0.19/docs/reference/metrics/) to help monitor the state of the system, and on-demand visibility endpoint for [monitoring of pending workloads](/v0.19/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/).
- **AdmissionChecks:** A mechanism for internal or external components to influence whether a workload can be [admitted](/v0.19/docs/concepts/admission_check/).
- **Advanced autoscaling support:** Integration with cluster-autoscaler's [provisioningRequest](/v0.19/docs/concepts/admission_check/provisioning_request/#job-using-a-provisioningrequest) via admissionChecks.
- **All-or-nothing with ready Pods:** A timeout-based implementation of [All-or-nothing scheduling](/v0.19/docs/tasks/manage/setup_wait_for_pods_ready/).
- **Partial admission and dynamic reclaim:** mechanisms to run a job with [reduced parallelism](/v0.19/docs/tasks/run/jobs/#partial-admission), based on available quota, and to [release](/v0.19/docs/concepts/workload/#dynamic-reclaim) the quota the pods complete..
- **Mixing training and inference**: Simultaneous management of batch workloads along with serving workloads (such as [Deployments](/v0.19/docs/tasks/run/deployment/) or [StatefulSets](/v0.19/docs/tasks/run/statefulset/))
- **Multi-cluster job dispatching:** called [MultiKueue](/v0.19/docs/concepts/multikueue/), allows to search for capacity and off-load the main cluster.
- **Topology-Aware Scheduling**: Allows to optimize the pod-pod communication throughput by [scheduling aware of the data-center topology](/v0.19/docs/concepts/topology_aware_scheduling/).

## Job-integrated features

| Feature                                                                                                         | Batch&nbsp;Job | JobSet | PaddleJob | PytorchJob | TFJob | TrainJob | XGBoostJob | MPIJob | JAXJob | Pod | RayCluster | RayJob | AppWrapper | Deployment | StatefulSet | LeaderWorkerSet |
|-----------------------------------------------------------------------------------------------------------------|----------------|--------|-----------|------------|-------|----------|------------|--------|:------:|-----|------------|--------|------------|------------|-------------|-----------------|
| [Dynamic Reclaim](/v0.19/docs/concepts/workload/#dynamic-reclaim)                                                     | +              | +      |           |            |       | +        |            |        |        | +   |            |        |            |            |             |                 |
| [MultiKueue](/v0.19/docs/concepts/multikueue/)                                                                        | +              | +      | +         | +          | +     | +        | +          | +      |   +    |     | +          | +      | +          |            | +           | +               |
| [PartialAdmission](/v0.19/docs/tasks/run/jobs/#partial-admission)                                                     | +              |        |           |            |       |          |            |        |        |     |            |        |            |            |             |                 |
| [Workload Priority Class](/v0.19/docs/concepts/workload_priority_class/)                                              | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [FlavorFungibility](/v0.19/docs/concepts/cluster_queue/#flavorfungibility)                                            | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [ProvisioningACC](/v0.19/docs/concepts/admission_check/provisioning_request/)                                         | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [VisibilityOnDemand](/v0.19/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/)                 | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [PrioritySortingWithinCohort](/v0.19/docs/concepts/cluster_queue/#flavors-and-borrowing-semantics)                    | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [LendingLimit](/v0.19/docs/concepts/cluster_queue/#lendinglimit)                                                      | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [All-or-nothing with ready Pods](/v0.19/docs/concepts/workload/#all-or-nothing-semantics-for-job-resource-assignment) | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [Fair Sharing](/v0.19/docs/concepts/preemption/#fair-sharing)                                                         | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |
| [Topology Aware Scheduling](/v0.19/docs/concepts/topology_aware_scheduling)                                           | +              | +      | +         | +          | +     | +        | +          | +      |   +    | +   | +          | +      | +          | +          | +           | +               |

## High-level Kueue operation

![High Level Kueue Operation](/images/theory-of-operation.svg)

To learn more about Kueue concepts, see the [concepts](/v0.19/docs/concepts) section.

To learn about different Kueue personas and what you can do with Kueue, see the [tasks](/v0.19/docs/tasks) section.
