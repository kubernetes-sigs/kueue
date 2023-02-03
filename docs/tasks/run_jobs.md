# Run Jobs

This page shows you how to run a Job in a Kubernetes cluster with Kueue enabled.

The intended audience for this page are [batch users](/docs/tasks#batch-user).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/setup/install.md).
- The cluster has [quotas configured](administer_cluster_quotas.md).

## 0. Identify the queues available in your namespace

Run the following command to list the Queues available in your namespace.

```shell
kubectl -n default get localqueues
```

The output is similar to the following:

```
NAME   CLUSTERQUEUE    PENDING WORKLOADS
main   cluster-queue   3
```

The [ClusterQueue](/docs/concepts/cluster_queue.md) defines the quotas for the
Queue.

## 1. Define the Job

Running a Job in Kueue is similar to [running a Job in a Kubernetes cluster](https://kubernetes.io/docs/tasks/job/)
without Kueue. However, you must consider the following differences:

- You should create the Job in a [suspended state](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job),
  as Kueue will decide when it's the best time to start the Job.
- You have to set the Queue you want to submit the Job to. Use the
 `kueue.x-k8s.io/queue-name` annotation.
- You should include the resource requests for each Job Pod.

Here is a sample Job with three Pods that just sleep for a few seconds.
This sample is also available in [config/samples/sample-job.yaml](/config/samples/sample-job.yaml).

```yaml
# sample-job.yaml
apiVersion: batch/v1
kind: Job
metadata:
  generateName: sample-job-
  annotations:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  parallelism: 3
  completions: 3
  suspend: true
  template:
    spec:
      containers:
      - name: dummy-job
        image: gcr.io/k8s-staging-perf-tests/sleep:latest
        args: ["30s"]
        resources:
          requests:
            cpu: 1
            memory: "200Mi"
      restartPolicy: Never
```

## 2. Run the Job

You can run the Job with the following command:

```shell
kubectl create -f sample-job.yaml
```

Internally, Kueue will create a corresponding [Workload](/docs/concepts/workload.md)
for this Job with a matching name.

```shell
kubectl -n default get workloads
```

The output will be similar to the following:

```
NAME               QUEUE         ADMITTED BY     AGE
sample-job-sl4bm   user-queue                    1s
```

## 3. (Optional) Monitor the status of the workload

You can see the workload status with the following command:

```shell
kubectl -n default describe workload sample-job-sl4bm
```

If the ClusterQueue doesn't have enough quota to run the workload, the output
will be similar to the following:

```
Name:         sample-job-jtz5f
Namespace:    default
Labels:       <none>
Annotations:  <none>
API Version:  kueue.x-k8s.io/v1alpha2
Kind:         Workload
Metadata:
  ...
Spec:
  ...
Status:
  Conditions:
    Last Probe Time:       2022-03-28T19:43:03Z
    Last Transition Time:  2022-03-28T19:43:03Z
    Message:               workload didn't fit
    Reason:                Pending
    Status:                False
    Type:                  Admitted
Events:               <none>
```

When the ClusterQueue has enough quota to run the workload, it will admit
the workload. To see if the workload was admitted, run the following command:

```shell
kubectl -n default get workloads
```

The output is similar to the following:

```
NAME               QUEUE         ADMITTED BY     AGE
sample-job-sl4bm   user-queue    cluster-queue   45s
```

To view the event for the workload admission, run the following command:

```shell
kubectl -n default describe workload sample-job-sl4bm
```

The output is similar to the following:

```
...
Events:
  Type    Reason    Age   From           Message
  ----    ------    ----  ----           -------
  Normal  Admitted  50s   kueue-manager  Admitted by ClusterQueue cluster-queue
```

To continue monitoring the workload progress, you can run the following command:

```shell
kubectl -n default describe workload sample-job-sl4bm
```

Once the workload has finished running, the output is similar to the following:

```
...
Status:
  Conditions:
    ...
    Last Probe Time:       2022-03-28T19:43:37Z                                                                                                                      
    Last Transition Time:  2022-03-28T19:43:37Z                                                                                                                      
    Message:               Job finished successfully                                                                                                                 
    Reason:                JobFinished                                                                                                                               
    Status:                True                                                                                                                                      
    Type:                  Finished
...
```

To review more details about the Job status, run the following command:

```shell
kubectl -n default describe job sample-job-sl4bm
```

The output is similar to the following:

```
Name:             sample-job-sl4bm
Namespace:        default
...
Start Time:       Mon, 28 Mar 2022 15:45:17 -0400
Completed At:     Mon, 28 Mar 2022 15:45:49 -0400
Duration:         32s
Pods Statuses:    0 Active / 3 Succeeded / 0 Failed
Pod Template:
  ...
Events:
  Type    Reason            Age   From                  Message
  ----    ------            ----  ----                  -------
  Normal  Suspended         22m   job-controller        Job suspended
  Normal  CreatedWorkload   22m   kueue-job-controller  Created Workload: default/sample-job-rxb6q
  Normal  SuccessfulCreate  19m   job-controller        Created pod: sample-job-rxb6q-7bqld
  Normal  Started           19m   kueue-job-controller  Admitted by clusterQueue cluster-queue
  Normal  SuccessfulCreate  19m   job-controller        Created pod: sample-job-rxb6q-7jw4z
  Normal  SuccessfulCreate  19m   job-controller        Created pod: sample-job-rxb6q-m7wgm
  Normal  Resumed           19m   job-controller        Job resumed
  Normal  Completed         18m   job-controller        Job completed
```

Since events have a timestamp with a resolution of seconds, the events might
be listed in a slightly different order from which they actually occurred.
