---
title: "ProvisioningRequest"
date: 2023-10-23
weight: 1
description: >
  A built-in admission check providing Kueue integration with cluster-autoscaler.
---

When AdmissionChecks or [TopologyAwareScheduling](docs/concepts/topology_aware_scheduling/) were not configured, Admissions were mainly based on quota checks - if sufficient quota existed, Kueue admitted the Workload. While quota reservation confirmed logical resource availability, it didn't guarantee that physical resources existed to schedule all Pods successfully. The [ProvisioningRequest AdmissionCheck](/docs/concepts/admission_check/provisioning_request/#provisioning-admissioncheck-controller) addresses this in cluster-autoscaler environments.

Kueue's enhanced admission requires two sequential checks:

1. **Quota Reservation:** Kueue validates the resource requests against ClusterQueue's available quota and resource flavors, reserves the required resources if available and locks the quota to prevent other Workloads from claiming it. This step verifies logical resource availability.
2. **Capacity Guarantee:** This step uses ProvisioningRequest and [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) (CA) to verify physical resource availability. 
    - The Kueue controller creates a ProvisioningRequest object by attaching the Workload's PodTemplates(optionally merged via [PodSetMergePolicy](/docs/concepts/admission_check/provisioning_request/#podset-merge-policy)) , applying [ProvisioningRequestConfig](/docs/concepts/admission_check/provisioning_request/#provisioningrequestconfig) settings, and setting owner reference to Workload.
    - Cluster Autoscaler receives ProvisioningRequest, checks actual cluster capacity, triggers scaling if needed and updates ProvisioningRequest status with this possible states: 
      - `Provisioned=true`: CA provisioned the capacity and it's ready to use
      - `Provisioned=false`: Provisioning in progress
      - `Failed=true`:  CA couldn't provision the capacity
      - `BookingExpired=true`: CA stopped booking the capacity, it will scale down if there are no Pods running on it  
      - `CapacityRevoked=true`: CA revokes the capacity, if a Workload is running on it, it will be evicted
  
    These conditions only affect non-admitted Workloads. Once admitted, they are ignored.


Let's understand this with a real-world usage - GPU Workload:

Scenario: *AI training job requiring 16 GPUs :*

- **Step 1** *(Quota Reservation)*: ClusterQueue has 32 GPU quota available. Kueue reserves 16 GPUs from this quota.

- **Step 2** *(Admission Check)*: Kueue creates a ProvisioningRequest requesting for 16 GPUs. 
  - Cluster Autoscaler checks cloud provider GPU inventory and initiates scaling of 4x GPU nodes (4 GPUs each). It sets `Provisioned=true` when nodes are ready.

  - Kueue sees the `Provisioned=true` marks the AdmissionCheck `Ready` and admits workload.

Outcome:
*Job starts immediately with all 16 GPUs available.*



## Provisioning AdmissionCheck Controller

The Provisioning AdmissionCheck Controller is an AdmissionCheck Controller designed to integrate Kueue with [Kubernetes cluster-autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler). Its primary function is to create [ProvisioningRequests](https://github.com/kubernetes/autoscaler/blob/4872bddce2bcc5b4a5f6a3d569111c11b8a2baf4/cluster-autoscaler/provisioningrequest/apis/autoscaling.x-k8s.io/v1beta1/types.go#L41) for the workloads holding [Quota Reservation](/docs/concepts/#quota-reservation) and keeping the [AdmissionCheckState](/docs/concepts/admission_check/#admissioncheckstate) in sync.

The controller is part of Kueue and is enabled by default. The feature is now Generally Available (GA) as of Kueue v0.14.

Check the list of supported Provisioning Classes and prerequisite for them in [ClusterAutoscaler documentation](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/FAQ.md#supported-provisioningclasses).

## Usage

To use the Provisioning AdmissionCheck, create an [AdmissionCheck](/docs/concepts/admission_check)
with `kueue.x-k8s.io/provisioning-request` as a `.spec.controllerName` and create a ProvisioningRequest configuration using a `ProvisioningRequestConfig` object.

Next, you need to reference the AdmissionCheck from the ClusterQueue, as detailed in [Admission Check usage](/docs/concepts/admission_check#usage).

See [below](#setup) for a full setup.

## ProvisioningRequest configuration

There are two ways to configure the ProvisioningRequests that Kueue creates for your Jobs.

- **ProvisioningRequestConfig:** This configuration in the AdmissionCheck applies to all the jobs that go through this check.
It enables you to set `provisioningClassName`, `managedResources`, and `parameters`
- **Job annotation**: This configuration enables you to set `parameters` to a specific job. If both the annotation and the ProvisioningRequestConfig refer to the same parameter, the annotation value takes precedence.

### ProvisioningRequestConfig
A `ProvisioningRequestConfig` looks like the following:

```yaml
apiVersion: kueue.x-k8s.io/v1beta2
kind: ProvisioningRequestConfig
metadata:
  name: prov-test-config
spec:
  provisioningClassName: check-capacity.autoscaling.x-k8s.io
  managedResources:
  - nvidia.com/gpu
  retryStrategy:
    backoffLimitCount: 2
    backoffBaseSeconds: 60
    backoffMaxSeconds: 1800
  podSetMergePolicy: IdenticalWorkloadSchedulingRequirements
```

Where:
- **provisioningClassName** - describes the different modes of provisioning the resources. Supported ProvisioningClasses are listed in [ClusterAutoscaler documentation](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/FAQ.md#supported-provisioningclasses), also check your cloud provider's documentation for other ProvisioningRequest classes they support.
- **managedResources** -  contains the list of resources managed by the autoscaling.
- **retryStrategy.backoffLimitCount** - indicates how many times ProvisioningRequest should be retried in case of failure. Defaults to 3.
- **retryStrategy.backoffBaseSeconds** - provides the base for calculating backoff time that ProvisioningRequest waits before being retried. Defaults to 60.
- **retryStrategy.backoffMaxSeconds** - indicates the maximum backoff time (in seconds) before retrying a ProvisioningRequest. Defaults to 1800.
- **podSetMergePolicy** - allows to merge similar PodSets into a single PodTemplate used by the ProvisioningRequest.
- **podSetUpdates** - allows to update the Workload's PodSets with nodeSelectors based on the successful ProvisioningRequest.
  This allows to restrict scheduling of the PodSets' pods to the newly provisioned nodes.

#### PodSet merge policy

{{% alert title="Note" color="primary" %}}
`podSetMergePolicy` feature is available in Kueue v0.12.0 version or newer.

It offers two options:
- `IdenticalPodTemplates` - merges only identical PodTemplates
- `IdenticalWorkloadSchedulingRequirements` - merges PodTemplates which have
  identical fields which are considered for defining the workload scheduling
  requirements. The PodTemplate fields which are considered as workload
  scheduling requirements: 
  - `spec.containers[*].resources.requests`
  - `spec.initContainers[*].resources.requests`
  - `spec.resources`
  - `spec.nodeSelector`
  - `spec.tolerations`
  - `spec.affinity`
  - `resourceClaims`

When the field is not set, the PodTemplates are not merged when creating the ProvisioningRequest, even if identical.

For example, setting the field as either `IdenticalPodTemplates` or `IdenticalWorkloadSchedulingRequirements`, 
allows to create a ProvisioningRequest with a single PodTemplate when using PyTorchJob as in this sample: [`sample-pytorchjob.yaml`](/docs/tasks/run/kubeflow/pytorchjobs/#sample-pytorchjob). 
{{% /alert %}}

#### Retry strategy

If a ProvisioningRequest fails, it may be retried after a backoff period.
The backoff time (in seconds) is calculated using the following formula, where `n` is the retry number (starting at 1):

```latex
time = min(backoffBaseSeconds^n, backoffMaxSeconds)
```

When a ProvisioningRequest fails, the quota reserved for a Workload is released, and the Workload needs to restart the
admission cycle.

#### PodSet updates

In order to restrict scheduling of the workload's Pods to the newly provisioned
nodes you can use the "podSetUpdates" API which allows to inject node selectors
to target the nodes.

For example:

```yaml
podSetUpdates:
  nodeSelector:
  - key: autoscaling.cloud-provider.com/provisioning-request
    valueFromProvisioningClassDetail: RequestKey
```

This snippet in ProvisioningRequestConfig instructs Kueue to update the Job's
PodTemplate, after provisioning, to target the newly provisioned nodes which
have the label: `autoscaling.cloud-provider.com/provisioning-request` with the
value coming from the [ProvisiongClassDetails](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1/types.go#L169) map, under the "RequestKey" key.

Note that, this assumes the provisioning class (which can be cloud-provider
specific) supports setting unique node label on the newly provisioned nodes.

#### Reference

Check the [API definition](https://github.com/kubernetes-sigs/kueue/blob/main/apis/kueue/v1beta1/provisioningrequestconfig_types.go) for more details.

### Job annotations

Another way to pass ProvisioningRequest's [parameters](https://github.com/kubernetes/autoscaler/blob/0130d33747bb329b790ccb6e8962eedb6ffdd0a8/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1/types.go#L115) is by using Job annotations. Every annotation with the ***provreq.kueue.x-k8s.io/*** prefix will be directly passed to created ProvisioningRequest. E.g. `provreq.kueue.x-k8s.io/ValidUntilSeconds: "60"` will pass `ValidUntilSeconds` parameter with the value of `60`. See more examples below.

Once Kueue creates a ProvisioningRequest for the job you submitted, modifying the value of annotations in the job will have no effect in the ProvisioningRequest.

## Example

### Setup

{{< include "examples/provisioning/provisioning-setup.yaml" "yaml" >}}

### Job using a ProvisioningRequest

{{< include "examples/provisioning/sample-job.yaml" "yaml" >}}

