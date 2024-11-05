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

* Quotas and policies for fair sharing among tenants.
* Resource fungibility: if a resource flavor is fully utilized, Kueue can admit the job using a different flavor.

A core design principle for Kueue is to avoid duplicating mature functionality in Kubernetes components and well-established third-party controllers. Autoscaling, pod-to-node scheduling and job lifecycle management are the responsibility of cluster-autoscaler, kube-scheduler and kube-controller-manager, respectively. Advanced admission control can be delegated to controllers such as gatekeeper.

## Features overview

- **Job management:** Support job queueing based on [priorities](/docs/concepts/workload/#priority) with different [strategies](/docs/concepts/cluster_queue/#queueing-strategy): `StrictFIFO` and `BestEffortFIFO`.
- **Resource management:** Support resource fair sharing and [preemption](/docs/concepts/cluster_queue/#preemption) with a variety of policies between different tenants.
- **Dynamic resource reclaim:** A mechanism to [release](/docs/concepts/workload/#dynamic-reclaim) quota as the pods of a Job complete.
- **Resource flavor fungibility:** Quota [borrowing or preemption](/docs/concepts/cluster_queue/#flavorfungibility) in ClusterQueue and Cohort.
- **Integrations:** Built-in support for popular jobs, e.g. [BatchJob](/docs/tasks/run/jobs/), [Kubeflow training jobs](/docs/tasks/run/kubeflow/), [RayJob](/docs/tasks/run/rayjobs/), [RayCluster](/docs/tasks/run/rayclusters/), [JobSet](/docs/tasks/run/jobsets/),  [plain Pod](/docs/tasks/run/plain_pods/).
- **System insight:** Built-in [prometheus metrics](/docs/reference/metrics/) to help monitor the state of the system, as well as Conditions.
- **AdmissionChecks:** A mechanism for internal or external components to influence whether a workload can be [admitted](/docs/concepts/admission_check/).
- **Advanced autoscaling support:** Integration with cluster-autoscaler's [provisioningRequest](/docs/admission-check-controllers/provisioning/#job-using-a-provisioningrequest) via admissionChecks.
- **All-or-nothing with ready Pods:** A timeout-based implementation of [All-or-nothing scheduling](/docs/tasks/manage/setup_wait_for_pods_ready/).
- **Partial admission:** Allows jobs to run with a [smaller parallelism](/docs/tasks/run/jobs/#partial-admission), based on available quota, if the application supports it.

## Job-integrated features

| Feature                                                                                                         | Batch&nbsp;Job | JobSet | MXJob | PaddleJob | PytorchJob | TFJob | XGBoostJob | MPIJob | Pod | RayCluster | RayJob |
|-----------------------------------------------------------------------------------------------------------------|----------------|--------|-------|-----------|------------|-------|------------|--------|-----|------------|--------|
| [Dynamic Reclaim](/docs/concepts/workload/#dynamic-reclaim)                                                     | +              | +      |       |           |            |       |            |        | +   |            |        |
| [MultiKueue](/docs/concepts/multikueue/)                                                                        | +              | +      |       | +         | +          | +     | +          | +      |     |            |        |
| [MultiKueueBatchJobWithManagedBy](/docs/concepts/multikueue/#multikueuebatchjobwithmanagedby-enabled)           | +              |        |       |           |            |       |            |        |     |            |        |
| [PartialAdmission](/docs/tasks/run/jobs/#partial-admission)                                                     | +              |        |       |           |            |       |            |        |     |            |        |
| [Workload Priority Class](/docs/concepts/workload_priority_class/)                                              | +              | +      | +     | +         | +          | +     | +          | +      | +   | +          | +      |
| [FlavorFungibility](/docs/concepts/cluster_queue/#flavorfungibility)                                            | +              | +      | +     | +         | +          | +     | +          | +      | +   | +          | +      |
| [ProvisioningACC](/docs/admission-check-controllers/provisioning/)                                              | +              | +      | +     | +         | +          | +     | +          | +      | +   | +          | +      |
| [QueueVisibility](/docs/tasks/manage/monitor_pending_workloads/pending_workloads_in_status/)                    | +              | +      | +     | +         | +          | +     | +          | +      | +   | +          | +      |
| [VisibilityOnDemand](/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/)                 | +              | +      | +     | +         | +          | +     | +          | +      | +   | +          | +      |
| [PrioritySortingWithinCohort](/docs/concepts/cluster_queue/#flavors-and-borrowing-semantics)                    | +              | +      | +     | +         | +          | +     | +          | +      | +   | +          | +      |
| [LendingLimit](/docs/concepts/cluster_queue/#lendinglimit)                                                      | +              | +      | +     | +         | +          | +     | +          | +      | +   | +          | +      |
| [All-or-nothing with ready Pods](/docs/concepts/workload/#all-or-nothing-semantics-for-job-resource-assignment) | +              | +      | +     | +         | +          | +     | +          | +      | +   | +          | +      |
| [Fair Sharing](/docs/concepts/preemption/#fair-sharing)                                                         | +              | +      | +     | +         | +          | +     | +          | +      | +   | +          | +      |
| [Topology Aware Scheduling](/docs/concepts/topology_aware_scheduling)                                           | +              | +      | +     | +         | +          | +     | +          | +      | +   | +          | +      |

## High-level Kueue operation

![High Level Kueue Operation](/images/theory-of-operation.svg)

To learn more about Kueue concepts, see the [concepts](/docs/concepts) section.

To learn about different Kueue personas and what you can do with Kueue, see the [tasks](/docs/tasks) section.
