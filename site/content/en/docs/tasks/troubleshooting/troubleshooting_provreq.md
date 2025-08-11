---
title: "Troubleshooting Provisioning Request in Kueue"
date: 2024-05-20
weight: 3
description: >
  Troubleshooting the status of a Provisioning Request in Kueue
---

This document helps you troubleshoot ProvisioningRequests, an API defined by [ClusterAutoscaler](https://github.com/kubernetes/autoscaler/blob/4872bddce2bcc5b4a5f6a3d569111c11b8a2baf4/cluster-autoscaler/provisioningrequest/apis/autoscaling.x-k8s.io/v1beta1/types.go#L41).

Kueue creates ProvisioningRequests via the [Provisioning Admission Check Controller](/docs/admission-check-controllers/provisioning/), and treats them like an [Admission Check](/docs/concepts/admission_check/). In order for Kueue to admit a Workload, the ProvisioningRequest created for it needs to succeed.

## Before you begin

Before you begin troubleshooting, make sure your cluster meets the following requirements:
- Your cluster has ClusterAutoscaler enabled and ClusterAutoscaler supports ProvisioningRequest API.
Check your cloud provider's documentation to determine the minimum versions that support ProvisioningRequest. If you use GKE, your cluster should be running version `1.28.3-gke.1098000` or newer.
- You use a type of nodes that support ProvisioningRequest. It may vary depending on your cloud provider.
- Kueue's version is `v0.5.3` or newer.
- ProvisioningRequest support is enabled. This feature has been enabled by default since Kueue `v0.7.0` and is Generally Available (GA) as of Kueue `v0.14.0`.

## Identifying the Provisioning Request for your job

See the [Troubleshooting Jobs guide](/docs/tasks/troubleshooting/troubleshooting_jobs/#identifying-the-workload-for-your-job), to learn how to identify the Workload for your job.

You can run the following command to see a brief state of a Provisioning Request (and other Admission Checks) in the `admissionChecks` field of the Workload's Status.

```bash
kubectl describe workload WORKLOAD_NAME
```

Kueue creates ProvisioningRequests using a naming pattern that helps you identify the request corresponding to your workload.

```
[NAME OF YOUR WORKLOAD]-[NAME OF THE ADMISSION CHECK]-[NUMBER OF RETRY]
```
e.g.
```bash
sample-job-2zcsb-57864-sample-admissioncheck-1
```

When nodes for your job are provisioned, Kueue will also add the annotation `cluster-autoscaler.kubernetes.io/consume-provisioning-request` to the `.admissionChecks[*].podSetUpdate[*]` field in Workload's status. The value of this annotation is the Provisioning Request's name.

The output of the `kubectl describe workload` command should look similar to the following:

```bash
[...]
Status:
  Admission Checks:
    Last Transition Time:  2024-05-22T10:47:46Z
    Message:               Provisioning Request was successfully provisioned.
    Name:                  sample-admissioncheck
    Pod Set Updates:
      Annotations:
        cluster-autoscaler.kubernetes.io/consume-provisioning-request:  sample-job-2zcsb-57864-sample-admissioncheck-1
        cluster-autoscaler.kubernetes.io/provisioning-class-name:       queued-provisioning.gke.io
      Name:                                                             main
    State:                                                              Ready
```

## What is the current state of my Provisioning Request?

One possible reason your job is not running might be that ProvisioningRequest is waiting to be provisioned.
To find out if this is the case you can view Provisioning Request's state by running the following command:

```bash
kubectl get provisioningrequest PROVISIONING_REQUEST_NAME
```

If this is the case, the output should look similar to the following:

```bash
NAME                                                 ACCEPTED   PROVISIONED   FAILED   AGE
sample-job-2zcsb-57864-sample-admissioncheck-1       True       False         False    20s
```

You can also view more detailed status of your ProvisioningRequest by running the following command:

```bash
kubectl describe provisioningrequest PROVISIONING_REQUEST_NAME
```

If your ProvisioningRequest fails to provision nodes, the error output may look similar to the following:
```bash
[...]
Status:
  Conditions:
    Last Transition Time:  2024-05-22T13:04:54Z
    Message:               Provisioning Request wasn't accepted.
    Observed Generation:   1
    Reason:                NotAccepted
    Status:                False
    Type:                  Accepted
    Last Transition Time:  2024-05-22T13:04:54Z
    Message:               Provisioning Request wasn't provisioned.
    Observed Generation:   1
    Reason:                NotProvisioned
    Status:                False
    Type:                  Provisioned
    Last Transition Time:  2024-05-22T13:06:49Z
    Message:               max cluster limit reached, nodepools out of resources: default-nodepool (cpu, memory)
    Observed Generation:   1
    Reason:                OutOfResources
    Status:                True
    Type:                  Failed
```

Note that the `Reason` and `Message` values for `Failed` condition may differ from your output, depending on the
reason that prevented the provisioning.

The Provisioning Request state is described in the `.conditions[*].status` field.
An empty field means ProvisinongRequest is still being processed by the ClusterAutoscaler.
Otherwise, it falls into one of the states listed below:
- `Accepted` - indicates that the ProvisioningRequest was accepted by ClusterAutoscaler, so ClusterAutoscaler will attempt to provision the nodes for it.
- `Provisioned` - indicates that all of the requested resources were created and are available in the cluster. ClusterAutoscaler will set this condition when the VM creation finishes successfully.
- `Failed` - indicates that it is impossible to obtain resources to fulfill this ProvisioningRequest. Condition Reason and Message will contain more details about what failed.
- `BookingExpired` - indicates that the ProvisioningRequest had Provisioned condition before and capacity reservation time is expired.
- `CapacityRevoked` - indicates that requested resources are not longer valid.

The states transitions are as follow:

![Provisioning Request's states](/images/prov-req-states.svg)

## Why a Provisioning Request is not created?

If Kueue did not create a Provisioning Request for your job, try checking the following requirements:

### a. Verify ProvisioningRequest support is available

Since Kueue v0.14.0, ProvisioningRequest support is Generally Available (GA) and always enabled. For older versions of Kueue (v0.7.0 to v0.13.x), the feature was enabled by default via the `ProvisioningACC` feature gate.

If you're using an older version of Kueue, you can verify the feature gate is enabled by running:

```bash
kubectl describe pod -n kueue-system kueue-controller-manager-
```

### b. Ensure your Workload has reserved quota

To check if your Workload has reserved quota in a ClusterQueue check your Workload's status by running the following command:

```bash
kubectl describe workload WORKLOAD_NAME
```

The output should be similar to the following:

```bash
[...]
Status:
  Conditions:
    Last Transition Time:  2024-05-22T10:26:40Z
    Message:               Quota reserved in ClusterQueue cluster-queue
    Observed Generation:   1
    Reason:                QuotaReserved
    Status:                True
    Type:                  QuotaReserved
```

If the output you get is similar to the following:

```bash
  Conditions:
    Last Transition Time:  2024-05-22T08:48:47Z
    Message:               couldn't assign flavors to pod set main: insufficient unused quota for memory in flavor default-flavor, 4396Mi more needed
    Observed Generation:   1
    Reason:                Pending
    Status:                False
    Type:                  QuotaReserved
```

This means you do not have sufficient free quota in your ClusterQueue.

Other reasons why your Workload has not reserved quota may relate to LocalQueue/ClusterQueue misconfiguration, e.g.:

```bash
Status:
  Conditions:
    Last Transition Time:  2024-05-22T08:57:09Z
    Message:               ClusterQueue cluster-queue doesn't exist
    Observed Generation:   1
    Reason:                Inadmissible
    Status:                False
    Type:                  QuotaReserved
```

You can check if ClusterQueues and LocalQueues are ready to admit your Workloads.
See the [Troubleshooting Queues](/docs/tasks/troubleshooting/troubleshooting_queues/) for more details.


### c. Ensure the Admission Check is active

To check if the Admission Check that your job uses is active run the following command:

```bash
kubectl describe admissionchecks ADMISSIONCHECK_NAME
```

Where `ADMISSIONCHECK_NAME` is a name configured in your ClusterQueue spec. See the [Admission Check documentation](/docs/concepts/admission_check/) for more details.

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

If none of the above steps resolves your problem, contact us at the [Slack `wg-batch` channel](https://kubernetes.slack.com/archives/C032ZE66A2X)
