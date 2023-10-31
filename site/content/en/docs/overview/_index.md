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

- **Job management:** support job queueing based on priorities with different strategies: `StrictFIFO` and `BestEffortFIFO`
- **Resource management:** support resource fair sharing and [preemption](https://kueue.sigs.k8s.io/docs/concepts/cluster_queue/#preemption) with a variety of policies between different tenants
- **Dynamic resource reclaim:** a mechanism help to improve resource utilization
- **Resource flavor fungibility:** quota borrowing or preemption in ClusterQueue and Cohort
- **Integrations:** native support for a plenty of popular jobs, e.g. `BatctJob`, `Kubeflow Jobs`, `RayJob`, `jobSet`,  plain `Pod`
- **System insight:** build-in prometheus metrics to help monitor the state of the system, as well as Conditions
- **AdmissionChecks:** a mechanism for internal or external components to influence whether a workload can be admitted
- **Advanced autoscaling support:** Integration with cluster-autoscaler's provisioningRequest via admissionChecks
- **Sequential admission:** a simple implementation of all-or-nothing scheduling
- **Partial admission:** support *min_available_pods* semantic scheduling

## High-level Kueue operation

![High Level Kueue Operation](/images/theory-of-operation.svg)

To learn more about Kueue concepts, see the [concepts](/docs/concepts) section.

To learn about different Kueue personas and what you can do with Kueue, see the [tasks](/docs/tasks) section.
