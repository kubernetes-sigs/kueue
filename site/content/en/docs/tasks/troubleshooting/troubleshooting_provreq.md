---
title: "Troubleshooting Provisioning Request"
date: 2024-05-20
weight: 3
description: >
  Troubleshooting the status of a Provisioning Request
---

This document helps you troubleshoot ProvisioningRequests, which is an API defined by [Cluster Autoscaler](https://github.com/kubernetes/autoscaler/blob/4872bddce2bcc5b4a5f6a3d569111c11b8a2baf4/cluster-autoscaler/provisioningrequest/apis/autoscaling.x-k8s.io/v1beta1/types.go#L41).

You can see the Provisioning Request [documentation](https://cloud.google.com/kubernetes-engine/docs/how-to/provisioningrequest).

Provisioning Requests are created in Kueue with [Provisioning Admission Check Controller](/docs/admission-check-controllers/provisioning/), and are treated by Kueue like an [Admission Check](/docs/concepts/admission_check/). It means Provisioning Requests needs to succeed for a Workload to be admitted.

## Before you begin

Before you begin troubleshooting, make sure your cluster meets the following requirements:
- Your cluster's version is at least 1.28.3-gke.1098000 with the Cluster Autoscaler at least 28.122.0
- Kueue's version is at least 0.5.0
- You have enabled the `ProvisioningACC` in [the feature gates configuration](/docs/installation/#change-the-feature-gates-configuration)
- Your node group enables it. It may vary depending on your cloud provider.

## Provisioning Request's state

You can run a following command to see a brief state of a Provisioning Request (and other Admission Checks) in the `Admission Checks` field of the Workload's Status.

```bash
kubectl describe workload WORKLOAD_NAME
```

Kueue adds the annotation `cluster-autoscaler.kubernetes.io/consume-provisioning-request` with Provisionig Request's name as a value, which allows identifying a corresponding Provisioning Request to your Workload. You can find this annotation in Workload's Status in the `Admission Checks` field.

One of the reason your job is not running is that ProvisioningRequest is waiting to be provisioned. To find out if this is the case you can view Provisioning Request's state by running the following command:

```bash
kubectl get provisioningrequest PROVISIONING_REQUEST_NAME
```

and more detailed version with a command:

```bash
kubectl describe provisioningrequest PROVISIONING_REQUEST_NAME
```

Provisioning Request state is described in the `.conditions[*].status` field.  it means it still being processed by the Cluster Autoscaler. Otherwise it falls into one the states listed below:
- `Accepted` - indicates that the ProvisioningRequest was accepted by ClusterAutoscaler, so ClusterAutoscaler will attempt to provision the nodes for it.
- Provisioned - indicates that all of the requested resources were created and are available in the cluster. Cluster Autoscaler will set this condition when the VM creation finishes successfully.
- Failed - indicates that it is impossible to obtain resources to fulfill this ProvisioningRequest.	Condition Reason and Message will contain more details about what failed.
- BookingExpired - indicates that the ProvisioningRequest had Provisioned condition before and capacity reservation time is expired.
- CapacityRevoked - indicates that requested resources are not longer valid.

The states transitions are as follow:

![Provisioning Request's states](/images/prov-req-states.svg)

## Provisioning Request is not created by Kueue

If your job does not create a corresponding Provisioning Request try checking following requirements:

### Ensure the Kueue's controller manager enables the `ProvisioningACC` feature gate

Run a following command to check whether your Kueue's controller manager enables the `ProvisioningACC` feature gate:

```bash
kubectl describe pod -n kueue-system kueue-controller-manager-
```

The args for kueue container should be similar to this:

```bash
    ...
    Args:
      --config=/controller_manager_config.yaml
      --zap-log-level=2
      --feature-gates=ProvisioningACC=true
```

### Ensure the ClusterQueue and the LocalQueue are active
You can check the state of ClusterQueues and LocalQueues if the queues are ready for the Workloads.
Please see the [Troubleshooting Queues](/docs/tasks/troubleshooting/troubleshooting_queues/) for more details.


### Ensure the Admission Check is active

To check if the Admission Check your job uses is active run the following command:

```bash
kubectl describe admissionchecks ADMISSIONCHECK_NAME
```

The status of the Admission Check should be similar to:

```bash
...
Status:
  Conditions:
    Last Transition Time:  2024-03-08T11:44:53Z
    Message:               The admission check is active
    Reason:                Active
    Status:                True
    Type:                  Active
```
