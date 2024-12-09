---
title: "Provisioning Admission Check Controller"
date: 2023-10-23
weight: 1
description: >
  An admission check controller providing kueue integration with cluster autoscaler.
---

The Provisioning AdmissionCheck Controller is an AdmissionCheck Controller designed to integrate Kueue with [Kubernetes cluster-autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler). Its primary function is to create [ProvisioningRequests](https://github.com/kubernetes/autoscaler/blob/4872bddce2bcc5b4a5f6a3d569111c11b8a2baf4/cluster-autoscaler/provisioningrequest/apis/autoscaling.x-k8s.io/v1beta1/types.go#L41) for the workloads holding [Quota Reservation](/docs/concepts/#quota-reservation) and keeping the [AdmissionCheckState](/docs/concepts/admission_check/#admissioncheckstate) in sync.

The controller is part of Kueue. It is enabled by default. You can disable it by editing the `ProvisioningACC` feature gate. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.

The Provisioning Admission Check Controller is supported on [Kubernetes cluster-autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) versions 1.29 and later. However, some cloud-providers may not have an implementation for it.

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
apiVersion: kueue.x-k8s.io/v1beta1
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
```

Where:
- **provisioningClassName** - describes the different modes of provisioning the resources. Supported ProvisioningClasses are listed in [ClusterAutoscaler documentation](https://github.com/kubernetes/autoscaler/blob/master/cluster-autoscaler/FAQ.md#supported-provisioningclasses), also check your cloud provider's documentation for other ProvisioningRequest classes they support.
- **managedResources** -  contains the list of resources managed by the autoscaling.
- **retryStrategy.backoffLimitCount** - indicates how many times ProvisioningRequest should be retried in case of failure. Defaults to 3.
- **retryStrategy.backoffBaseSeconds** - provides the base for calculating backoff time that ProvisioningRequest waits before being retried. Defaults to 60.
- **retryStrategy.backoffMaxSeconds** - indicates the maximum backoff time (in seconds) before retrying a ProvisioningRequest. Defaults to 1800.

If a ProvisioningRequest fails, it may be retried after a backoff period.
The backoff time (in seconds) is calculated using the following formula, where n is the retry number (starting at 1):

$$
time = min(backoffBaseSeconds^n, backoffMaxSeconds)
$$

When a ProvisioningRequest fails, the quota reserved for a Workload is released, and the Workload needs to restart the
admission cycle. This behavior can be changed by enabling the KeepQuotaForProvReqRetry=true feature gate.
This feature gate ensures that the allocated quota is retained, but it may lead to underutilization of cluster resources.

Check the [API definition](https://github.com/kubernetes-sigs/kueue/blob/main/apis/kueue/v1beta1/provisioningrequestconfig_types.go) for more details.

### Job annotations

Another way to pass ProvisioningRequest's [parameters](https://github.com/kubernetes/autoscaler/blob/0130d33747bb329b790ccb6e8962eedb6ffdd0a8/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1beta1/types.go#L115) is by using Job annotations. Every annotation with the ***provreq.kueue.x-k8s.io/*** prefix will be directly passed to created ProvisioningRequest. E.g. `provreq.kueue.x-k8s.io/ValidUntilSeconds: "60"` will pass `ValidUntilSeconds` parameter with the value of `60`. See more examples below.

Once Kueue creates a ProvisioningRequest for the job you submitted, modifying the value of annotations in the job will have no effect in the ProvisioningRequest.

## Example

### Setup

{{< include "examples/provisioning/provisioning-setup.yaml" "yaml" >}}

### Job using a ProvisioningRequest

{{< include "examples/provisioning/sample-job.yaml" "yaml" >}}
