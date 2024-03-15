---
title: "Provisioning Admission Check Controller"
date: 2023-10-23
weight: 1
description: >
  An admission check controller providing kueue integration with cluster autoscaler.
---

The Provisioning Admission Check Controller is an Admission Check Controller designed to integrate Kueue with [Kubernetes cluster-autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler). Its primary function is to create [ProvisioningRequests](https://github.com/kubernetes/autoscaler/blob/4872bddce2bcc5b4a5f6a3d569111c11b8a2baf4/cluster-autoscaler/provisioningrequest/apis/autoscaling.x-k8s.io/v1beta1/types.go#L41) for the workloads holding [Quota Reservation](/docs/concepts/#quota-reservation) and keeping the [AdmissionCheckState](/docs/concepts/admission_check/#admissioncheckstate) in sync.

The controller is part of kueue. You can enable it by setting the `ProvisioningACC` feature gate. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.

The Provisioning Admission Check Controller is supported on [Kubernetes cluster-autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler) versions 1.29 and later. However, some cloud-providers may not have an implementation for it.

## Parameters

This controller uses a `ProvisioningRequestConfig` as parameters, like:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ProvisioningRequestConfig
metadata:
  name: prov-test-config
spec:
  provisioningClassName: queued-provisioning.gke.io
  managedResources:
  - nvidia.com/gpu
```

Where:
- **provisioningClassName** - describes the different modes of provisioning the resources. Check `autoscaling.x-k8s.io` `ProvisioningRequestSpec.provisioningClassName` for details.
- **managedResources** -  contains the list of resources managed by the autoscaling.

Check the [API definition](https://github.com/kubernetes-sigs/kueue/blob/main/apis/kueue/v1beta1/provisioningrequestconfig_types.go) for more details.

## Example

### Setup

{{< include "/examples/provisioning/provisioning-setup.yaml" "yaml" >}}

### Job using a ProvisioningRequest

{{< include "/examples/provisioning/sample-job.yaml" "yaml" >}}
