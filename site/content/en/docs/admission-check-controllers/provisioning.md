---
title: "Provisioning Admission Check Controller"
date: 2023-10-23
weight: 1
description: >
  An admission check controller providing kueue integration with cluster autoscaler.
---

The Provisioning Admission Check Controller is an Admission Check Controller designed to provide Kueues integration with [Kubernetes cluster autoscaler](https://github.com/kubernetes/autoscaler/tree/master/cluster-autoscaler). It's mainly focused on creating [ProvisioningRequests](https://github.com/kubernetes/autoscaler/blob/4872bddce2bcc5b4a5f6a3d569111c11b8a2baf4/cluster-autoscaler/provisioningrequest/apis/autoscaling.x-k8s.io/v1beta1/types.go#L41) for the workloads holding [Quota Reservation](docs/concepts/#quota-reservation) and keeping the [AdmissionCheckState](/docs/concepts/admission_check/#admissioncheckstate) in sync.

The controller is part of kueue and configured by `ProvisioningACC` feature gate, check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide on details on feature gate configuration.

## Parameters

This controller uses a `ProvisioningRequestConfig` as parameters, which uses the following schema:

```yaml
      openAPIV3Schema:
        description: ProvisioningRequestConfig is the Schema for the provisioningrequestconfig
          API
        properties:
          spec:
            description: ProvisioningRequestConfigSpec defines the desired state of
              ProvisioningRequestConfig
            properties:
              managedResources:
                description: "managedResources contains the list of resources managed
                  by the autoscaling. \n If empty, all resources are considered managed.
                  \n If not empty, the ProvisioningRequest will contain only the podsets
                  that are requesting at least one of them. \n If none of the workloads
                  podsets is requesting at least a managed resource, the workload
                  is considered ready."
                items:
                  description: ResourceName is the name identifying various resources
                    in a ResourceList.
                  type: string
                maxItems: 100
                type: array
                x-kubernetes-list-type: set
              parameters:
                additionalProperties:
                  description: Parameter is limited to 255 characters.
                  maxLength: 255
                  type: string
                description: Parameters contains all other parameters classes may
                  require.
                maxProperties: 100
                type: object
              provisioningClassName:
                description: ProvisioningClassName describes the different modes of
                  provisioning the resources. Check autoscaling.x-k8s.io ProvisioningRequestSpec.ProvisioningClassName
                  for details.
                maxLength: 253
                pattern: ^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$
                type: string
            required:
            - provisioningClassName
            type: object
        type: object
```

## Example

### Setup

{{< include "/examples/provisioning/provisioning-setup.yaml" "yaml" >}}

### Job

{{< include "/examples/provisioning/sample-job.yaml" "yaml" >}}
