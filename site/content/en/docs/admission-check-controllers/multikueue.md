---
title: "MultiKueue Admission Check Controller"
date: 2024-02-08
weight: 2
description: >
  An admission check controller providing the manager side MultiKueue functionality.
---

The MultiKueue Admission Check Controller is an Admission Check Controller designed to provide the manager side functionality for [MultiKueue](https://github.com/kubernetes-sigs/kueue/blob/main/keps/693-multikueue/README.md). 

The controller is part of kueue. You can enable it by setting the `MultiKueue` feature gate. Check the [Installation](/docs/installation/#change-the-feature-gates-configuration) guide for details on feature gate configuration.

{{% alert title="Warning" color="warning" %}}
MultiKueue is currently an alpha feature and disabled by default.
{{% /alert %}}

The controller's main attributes are:
- Establish and maintain the connection with the worker clusters.
- Maintain the `Active` status of the Admission Checks controlled by `multikueue`
- Create and monitor remote objects (workloads or jobs) while keeping the local ones in sync.

## Supported jobs
### batch/Job
Known Limitations:
- Since unsuspending a Job in the manager cluster will lead to its local execution, the AdmissionCheckStates are kept `Pending` during the remote job execution.
- Since updating the status of a local Job could conflict with the Job's main controller, the Job status is not synced during the job execution, the final status of the remote Job is copied when the remote workload is marked as `Finished`.

There is an ongoing effort to overcome these limitations by adding the possibility to disable the reconciliation of some jobs by the main `batch/Job` controller. Details in `kubernetes/enhancements` [KEP-4368](https://github.com/kubernetes/enhancements/tree/master/keps/sig-apps/4368-support-managed-by-label-for-batch-jobs#readme).

### JobSet
Known Limitations:
- Since unsuspending a JobSet in the manager cluster will lead to its local execution and updating the status of a local JobSet could conflict with its main controller, only the JobSet CRDs should be installed in the manager cluster.
An approach similar to the one described for [`batch/Job`](#batchjob) is taken into account to overcome this. 

## Parameters

An AdmissionCheck controlled by `multikueue` should use a `kueue.x-k8s.io/v1alpha1` `MultiKueueConfig` parameters object, details of its structure can be found in [Kueue Alpha API reference section](/docs/reference/kueue-alpha.v1alpha1/#kueue-x-k8s-io-v1alpha1-MultiKueueConfig)

## Example

### Setup

{{< include "/examples/multikueue/multikueue-setup.yaml" "yaml" >}}

For the example provided, having the worker1 cluster kubeconfig stored in a file called `worker1.kubeconfig`, the 
`worker1-secret` secret ca by created with:

```bash
 kubectl create secret generic worker1-secret -n kueue-system --from-file=kubeconfig=worker1.kubeconfig
```
