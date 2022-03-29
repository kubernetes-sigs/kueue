# Run Jobs

This page show you how to run a Job in a Kubernetes cluster with Kueue enabled.

The intended audience for this page are [batch users](/docs/tasks/README.md#as-a-batch-user)

## Before you begin

You need to have a Kubernetes cluster, the kubectl command-line tool
must be configured to communicate with your cluster, and [Kueue installed](/README.md#installation).

The cluster should have [quotas configured](administer_cluster_quotas.md).

## 0. Identify the queues available in your namespace

Run the following command to list the Queues available in your namespace.

```shell
kubectl -n default get queues
```

The output is similar to:

```
NAME   CLUSTERQUEUE    PENDING WORKLOADS
main   cluster-total   3
```

The [ClusterQueue](/docs/concepts/cluster_queue.md) defines the quotas for the
Queue.

## 1. Define the Job

Running a Job in Kueue is not much different from [running a Job in a Kubernetes cluster](https://kubernetes.io/docs/tasks/job/)
without Kueue. The differences are:
- You should create the Job in a [suspended state](https://kubernetes.io/docs/concepts/workloads/controllers/job/#suspending-a-job),
  as Kueue will decide when it's the best time to start it.
- You have to set the Queue you want to submit the Job to. This is done through
  the annotation `kueue.x-k8s.io/queue-name`.
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
    kueue.x-k8s.io/queue-name: main
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

Internally, Kueue will create a corresponding [QueuedWorkload](/docs/concepts/queued_workload.md)
for this Job with a matching name.

```shell
kubectl -n default get workloads
```

The output will be similar to:

```
NAME               QUEUE   ADMITTED BY     AGE
sample-job-sl4bm   main                    1s
```

## 3. (Optional) Monitor the status of the workload

You can run the following command to see the status of the workload.

```shell
kubectl -n default describe workload sample-job-sl4bm
```

If the ClusterQueue doesn't have enough quota to run the workload, the output
will be similar to:

```
Name:         sample-job-jtz5f                                                                                                                                       
Namespace:    default                                                                                                                                                
Labels:       <none>                                                                                                                                                 
Annotations:  <none>                                                                                                                                                 
API Version:  kueue.x-k8s.io/v1alpha1                                                                                                                                
Kind:         QueuedWorkload                                                                                                                                         
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

After some time, when the ClusterQueue has enough quota to run the workload,
the output of `kubectl -n default get workloads` will look similar to:

```
NAME               QUEUE   ADMITTED BY     AGE
sample-job-sl4bm   main    cluster-total   45s
```

And when you run `kubectl -n default describe workload sample-job-sl4bm` again,
you will observe an event for the workload admission.

```
...
Events:
  Type    Reason    Age   From           Message
  ----    ------    ----  ----           -------
  Normal  Admitted  50s   kueue-manager  Admitted by ClusterQueue cluster-total
```

Finally, once the workload has finished executing, you will observe a status
condition.

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

You can find other details in the Job status by running:

```shell
kubectl -n default describe job sample-job-sl4bm
```

The output will be similar to:

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
  Type    Reason                 Age   From                  Message
  ----    ------                 ----  ----                  -------
  Normal  Suspended              22m   job-controller        Job suspended
  Normal  CreatedQueuedWorkload  22m   kueue-job-controller  Created QueuedWorkload: default/sample-job-rxb6q
  Normal  SuccessfulCreate       19m   job-controller        Created pod: sample-job-rxb6q-7bqld
  Normal  Started                19m   kueue-job-controller  Admitted by clusterQueue cluster-total
  Normal  SuccessfulCreate       19m   job-controller        Created pod: sample-job-rxb6q-7jw4z
  Normal  SuccessfulCreate       19m   job-controller        Created pod: sample-job-rxb6q-m7wgm
  Normal  Resumed                19m   job-controller        Job resumed
  Normal  Completed              18m   job-controller        Job completed
```