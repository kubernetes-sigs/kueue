---
title: "Setup All-or-nothing with ready Pods"
date: 2022-03-14
weight: 5
description: >
  Timeout-based implementation of the All-or-nothing scheduling
---

Some jobs need all pods to be running at the same time to operate; for example,
synchronized distributed training or MPI-based jobs which require pod-to-pod
communication. On a default Kueue configuration, a pair of such jobs may deadlock
if the physical availability of resources do not match the configured quotas in
Kueue. The same pair of jobs could run to completion if their pods were scheduled
sequentially.

To address this requirement, in version 0.3.0 we introduced an opt-in mechanism
configured via the flag `waitForPodsReady` that provides a simple implementation
of the all-or-nothing scheduling. When enabled, the workload is monitored by
Kueue until all of its Pods are ready (meaning scheduled, running, and passing
the optional readiness probe). If not all pods of the workload are ready within
the configured timeout, then the workload is evicted and requeued.

This page shows you how to configure Kueue to use `waitForPodsReady`, which
is a simple implementation of the all-or-nothing scheduling.
The intended audience for this page are [batch administrators](/docs/tasks#batch-administrator).

## Before you begin

Make sure the following conditions are met:

- A Kubernetes cluster is running.
- The kubectl command-line tool has communication with your cluster.
- [Kueue is installed](/docs/installation) in version 0.3.0 or later.

## Enabling waitForPodsReady

Follow the instructions described
[here](/docs/installation#install-a-custom-configured-released-version) to
install a release version by extending the configuration with the following
fields:

```yaml
    waitForPodsReady:
      enable: true
      timeout: 10m
      blockAdmission: true
      requeuingStrategy:
        timestamp: Eviction | Creation
        backoffLimitCount: 5
        backoffBaseSeconds: 60
        backoffMaxSeconds: 3600
```

{{% alert title="Note" color="primary" %}}
If you update an existing Kueue installation you may need to restart the
`kueue-controller-manager` pod in order for Kueue to pick up the updated
configuration. In that case run:

```shell
kubectl delete pods --all -n kueue-system
```
{{% /alert %}}

The `timeout` (`waitForPodsReady.timeout`) is an optional parameter, defaulting to
5 minutes.

When the `timeout` expires for an admitted Workload, and the workload's
pods are not all scheduled yet (i.e., the Workload condition remains
`PodsReady=False`), then the Workload's admission is
cancelled, the corresponding job is suspended and the Workload is re-queued.

The `blockAdmission` (`waitForPodsReady.blockAdmission`) is an optional parameter.
When enabled, then the workloads are admitted sequentially to prevent deadlock
situations as demonstrated in the example below.

### Requeuing Strategy

{{< feature-state state="stable" for_version="v0.6" >}}

{{% alert title="Note" color="primary" %}}
The `backoffBaseSeconds` and `backoffMaxSeconds` are available in Kueue v0.7.0 and later
{{% /alert %}}

The `requeuingStrategy` (`waitForPodsReady.requeuingStrategy`) contains optional parameters:
- `timestamp`
- `backoffLimitCount`
- `backoffBaseSeconds`
- `backoffMaxSeconds`

The `timestamp` field defines which timestamp Kueue uses to order the Workloads in the queue:

- `Eviction` (default): The `lastTransitionTime` of the `Evicted=true` condition with `PodsReadyTimeout` reason in a Workload.
- `Creation`: The creationTimestamp in a Workload.

If you want to re-queue a Workload evicted by the `PodsReadyTimeout` back to its original place in the queue,
you should set the timestamp to the `Creation`.

Kueue will re-queue a Workload evicted by the `PodsReadyTimeout` reason until the number of re-queues reaches `backoffLimitCount`.
If you don't specify any value for `backoffLimitCount`,
a Workload is repeatedly and endlessly re-queued to the queue based on the `timestamp`.
Once the number of re-queues reaches the limit, Kueue [deactivates the Workload](/docs/concepts/workload/#active).

The time to re-queue a workload after each consecutive timeout is increased
exponentially, with the exponent of 2. The first delay is determined by the
`backoffBaseSeconds` parameter (defaulting to 60). You can configure the maximum
backoff time by setting the `backoffMaxSeconds` (defaulting to 3600). Using the defaults, the
evicted workload is re-queued after approximately `60, 120, 240, ..., 3600, ..., 3600` seconds.
Even if the backoff time reaches the `backoffMaxSeconds`, Kueue will continue to re-queue an evicted Workload with the `backoffMaxSeconds`
until the number of re-queue reaches the `backoffLimitCount`.

## Example

In this example we demonstrate the impact of enabling `waitForPodsReady` in Kueue.
We create two jobs which both require all their pods to be running at the same
time to complete. The cluster has enough resources to support running one of the
jobs at the same time, but not both.

{{% alert title="Note" color="primary" %}}
In this example we use a cluster with autoscaling disabled in order to simulate
issues with resource provisioning to satisfy the configured cluster quota.
{{% /alert %}}

### 1. Preparation

First, check the amount of allocatable memory in your cluster. In many cases this
can be done with this command:

```shell
TOTAL_ALLOCATABLE=$(kubectl get node --selector='!node-role.kubernetes.io/master,!node-role.kubernetes.io/control-plane' -o jsonpath='{range .items[*]}{.status.allocatable.memory}{"\n"}{end}' | numfmt --from=auto | awk '{s+=$1} END {print s}')
echo $TOTAL_ALLOCATABLE
```

In our case this outputs `8838569984` which, for the purpose of the example, can
be approximated as `8429Mi`.

#### Configure ClusterQueue quotas

We configure the memory flavor by doubling the total memory allocatable in
our cluster, in order to simulate issues with provisioning.

Save the following cluster queues configuration as `cluster-queues.yaml`:

``` yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: ResourceFlavor
metadata:
  name: "default-flavor"
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: ClusterQueue
metadata:
  name: "cluster-queue"
spec:
  namespaceSelector: {}
  resourceGroups:
  - coveredResources: ["memory"]
    flavors:
    - name: "default-flavor"
      resources:
      - name: "memory"
        nominalQuota: 16858Mi # double the value of allocatable memory in the cluster
---
apiVersion: kueue.x-k8s.io/v1beta1
kind: LocalQueue
metadata:
  namespace: "default"
  name: "user-queue"
spec:
  clusterQueue: "cluster-queue"
```

Then, apply the configuration by:

```shell
kubectl apply -f cluster-queues.yaml
```

#### Prepare the job template

Save the following job template in the `job-template.yaml` file. Note the
`_ID_` placeholders which will be replaced to create configurations for the
two jobs. Also, pay attention to configure the memory field for the container
as be 75% of the total allocatable memory per pod. In our case this is
`75%*(8429Mi/20)=316Mi`. In this scenario there is not enough resources to
run all pods for both jobs at the same time, risking deadlock.

```yaml
apiVersion: v1
kind: Service
metadata:
  name: svc_ID_
spec:
  clusterIP: None
  selector:
    job-name: job_ID_
  ports:
  - name: http
    protocol: TCP
    port: 8080
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: script-code_ID_
data:
  main.py: |
    from http.server import BaseHTTPRequestHandler, HTTPServer
    from urllib.request import urlopen
    import sys, os, time, logging

    logging.basicConfig(stream=sys.stdout, level=logging.DEBUG)
    serverPort = 8080
    INDEX_COUNT = int(sys.argv[1])
    index = int(os.environ.get('JOB_COMPLETION_INDEX'))
    logger = logging.getLogger('LOG' + str(index))

    class WorkerServer(BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200)
            self.end_headers()
            if "exit" in self.path:
              self.wfile.write(bytes("Exiting", "utf-8"))
              self.wfile.close()
              sys.exit(0)
            else:
              self.wfile.write(bytes("Running", "utf-8"))

    def call_until_success(url):
      while True:
        try:
          logger.info("Calling URL: " + url)
          with urlopen(url) as response:
            response_content = response.read().decode('utf-8')
            logger.info("Response content from %s: %s" % (url, response_content))
            return
        except Exception as e:
          logger.warning("Got exception when calling %s: %s" % (url, e))
        time.sleep(1)

    if __name__ == "__main__":
      if index == 0:
        for i in range(1, INDEX_COUNT):
          call_until_success("http://job_ID_-%d.svc_ID_:8080/ping" % i)
        logger.info("All workers running")

        time.sleep(10) # sleep 10s to simulate doing something

        for i in range(1, INDEX_COUNT):
          call_until_success("http://job_ID_-%d.svc_ID_:8080/exit" % i)
        logger.info("All workers stopped")
      else:
        webServer = HTTPServer(("", serverPort), WorkerServer)
        logger.info("Server started at port %s" % serverPort)
        webServer.serve_forever()
---

apiVersion: batch/v1
kind: Job
metadata:
  name: job_ID_
  labels:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  parallelism: 20
  completions: 20
  completionMode: Indexed
  suspend: true
  template:
    spec:
      subdomain: svc_ID_
      volumes:
      - name: script-volume
        configMap:
          name: script-code_ID_
      containers:
      - name: main
        image: python:bullseye
        command: ["python"]
        args:
        - /script-path/main.py
        - "20"
        ports:
        - containerPort: 8080
        imagePullPolicy: IfNotPresent
        resources:
          requests:
            memory: "316Mi" # choose the value as 75% * (total allocatable memory / 20)
        volumeMounts:
          - mountPath: /script-path
            name: script-volume
      restartPolicy: Never
  backoffLimit: 0
```

#### Additional quick job

We also prepare an additional job to increase the variance in the timings to
make the deadlock more likely. Save the following yaml as `quick-job.yaml`:

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: quick-job
  annotations:
    kueue.x-k8s.io/queue-name: user-queue
spec:
  parallelism: 50
  completions: 50
  suspend: true
  template:
    spec:
      restartPolicy: Never
      containers:
      - name: sleep
        image: bash:5
        command: ["bash"]
        args: ["-c", 'echo "Hello world"']
        resources:
          requests:
            memory: "1"
  backoffLimit: 0
```

### 2. Induce a deadlock under the default configuration (optional)

#### Run the jobs

```yaml
sed 's/_ID_/1/g' job-template.yaml > /tmp/job1.yaml
sed 's/_ID_/2/g' job-template.yaml > /tmp/job2.yaml
kubectl create -f quick-job.yaml
kubectl create -f /tmp/job1.yaml
kubectl create -f /tmp/job2.yaml
```

After a while check the status of the pods by

```shell
kubectl get pods
```

The output is like this (omitting the pods of the `quick-job` for brevity):

```shell
NAME            READY   STATUS      RESTARTS   AGE
job1-0-9pvs8    1/1     Running     0          28m
job1-1-w9zht    1/1     Running     0          28m
job1-10-fg99v   1/1     Running     0          28m
job1-11-4gspm   1/1     Running     0          28m
job1-12-w5jft   1/1     Running     0          28m
job1-13-8d5jk   1/1     Running     0          28m
job1-14-h5q8x   1/1     Running     0          28m
job1-15-kkv4j   0/1     Pending     0          28m
job1-16-frs8k   0/1     Pending     0          28m
job1-17-g78g8   0/1     Pending     0          28m
job1-18-2ghmt   0/1     Pending     0          28m
job1-19-4w2j5   0/1     Pending     0          28m
job1-2-9s486    1/1     Running     0          28m
job1-3-s9kh4    1/1     Running     0          28m
job1-4-52mj9    1/1     Running     0          28m
job1-5-bpjv5    1/1     Running     0          28m
job1-6-7f7tj    1/1     Running     0          28m
job1-7-pnq7w    1/1     Running     0          28m
job1-8-7s894    1/1     Running     0          28m
job1-9-kz4gt    1/1     Running     0          28m
job2-0-x6xvg    1/1     Running     0          28m
job2-1-flkpj    1/1     Running     0          28m
job2-10-vf4j9   1/1     Running     0          28m
job2-11-ktbld   0/1     Pending     0          28m
job2-12-sf4xb   1/1     Running     0          28m
job2-13-9j7lp   0/1     Pending     0          28m
job2-14-czc6l   1/1     Running     0          28m
job2-15-m77zt   0/1     Pending     0          28m
job2-16-7p7fs   0/1     Pending     0          28m
job2-17-sfdmj   0/1     Pending     0          28m
job2-18-cs4lg   0/1     Pending     0          28m
job2-19-x66dt   0/1     Pending     0          28m
job2-2-hnqjv    1/1     Running     0          28m
job2-3-pkwhw    1/1     Running     0          28m
job2-4-gdtsh    1/1     Running     0          28m
job2-5-6swdc    1/1     Running     0          28m
job2-6-qb6sp    1/1     Running     0          28m
job2-7-grcg4    0/1     Pending     0          28m
job2-8-kg568    1/1     Running     0          28m
job2-9-hvwj8    0/1     Pending     0          28m
```

These jobs are now deadlock'ed and are not going to be able to make progress.

#### Cleanup

Clean up the jobs by:

```shell
kubectl delete -f quick-job.yaml
kubectl delete -f /tmp/job1.yaml
kubectl delete -f /tmp/job2.yaml
```

### 3. Run with waitForPodsReady enabled

#### Enable waitForPodsReady

Update the Kueue configuration following the instructions [here](#enabling-waitforpodsready).

#### Run the jobs

Run the `start.sh` script

```yaml
sed 's/_ID_/1/g' job-template.yaml > /tmp/job1.yaml
sed 's/_ID_/2/g' job-template.yaml > /tmp/job2.yaml
kubectl create -f quick-job.yaml
kubectl create -f /tmp/job1.yaml
kubectl create -f /tmp/job2.yaml
```

#### Monitor the progress

Execute the following command in a couple of seconds internals to monitor
the progress:

```shell
kubectl get pods
```

We omit the pods of the completed `quick` job for brevity.

Output when `job1` is starting up, note that `job2` remains suspended:

```shell
NAME            READY   STATUS              RESTARTS   AGE
job1-0-gc284    0/1     ContainerCreating   0          1s
job1-1-xz555    0/1     ContainerCreating   0          1s
job1-10-2ltws   0/1     Pending             0          1s
job1-11-r4778   0/1     ContainerCreating   0          1s
job1-12-xx8mn   0/1     Pending             0          1s
job1-13-glb8j   0/1     Pending             0          1s
job1-14-gnjpg   0/1     Pending             0          1s
job1-15-dzlqh   0/1     Pending             0          1s
job1-16-ljnj9   0/1     Pending             0          1s
job1-17-78tzv   0/1     Pending             0          1s
job1-18-4lhw2   0/1     Pending             0          1s
job1-19-hx6zv   0/1     Pending             0          1s
job1-2-hqlc6    0/1     ContainerCreating   0          1s
job1-3-zx55w    0/1     ContainerCreating   0          1s
job1-4-k2tb4    0/1     Pending             0          1s
job1-5-2zcw2    0/1     ContainerCreating   0          1s
job1-6-m2qzw    0/1     ContainerCreating   0          1s
job1-7-hgp9n    0/1     ContainerCreating   0          1s
job1-8-ss248    0/1     ContainerCreating   0          1s
job1-9-nwqmj    0/1     ContainerCreating   0          1s
```

Output when `job1` is running and `job2` is now unsuspended as `job` has all
the required resources assigned:

```shell
NAME            READY   STATUS      RESTARTS   AGE
job1-0-gc284    1/1     Running     0          9s
job1-1-xz555    1/1     Running     0          9s
job1-10-2ltws   1/1     Running     0          9s
job1-11-r4778   1/1     Running     0          9s
job1-12-xx8mn   1/1     Running     0          9s
job1-13-glb8j   1/1     Running     0          9s
job1-14-gnjpg   1/1     Running     0          9s
job1-15-dzlqh   1/1     Running     0          9s
job1-16-ljnj9   1/1     Running     0          9s
job1-17-78tzv   1/1     Running     0          9s
job1-18-4lhw2   1/1     Running     0          9s
job1-19-hx6zv   1/1     Running     0          9s
job1-2-hqlc6    1/1     Running     0          9s
job1-3-zx55w    1/1     Running     0          9s
job1-4-k2tb4    1/1     Running     0          9s
job1-5-2zcw2    1/1     Running     0          9s
job1-6-m2qzw    1/1     Running     0          9s
job1-7-hgp9n    1/1     Running     0          9s
job1-8-ss248    1/1     Running     0          9s
job1-9-nwqmj    1/1     Running     0          9s
job2-0-djnjd    1/1     Running     0          3s
job2-1-trw7b    0/1     Pending     0          2s
job2-10-228cc   0/1     Pending     0          2s
job2-11-2ct8m   0/1     Pending     0          2s
job2-12-sxkqm   0/1     Pending     0          2s
job2-13-md92n   0/1     Pending     0          2s
job2-14-4v2ww   0/1     Pending     0          2s
job2-15-sph8h   0/1     Pending     0          2s
job2-16-2nvk2   0/1     Pending     0          2s
job2-17-f7g6z   0/1     Pending     0          2s
job2-18-9t9xd   0/1     Pending     0          2s
job2-19-tgf5c   0/1     Pending     0          2s
job2-2-9hcsd    0/1     Pending     0          2s
job2-3-557lt    0/1     Pending     0          2s
job2-4-k2d6b    0/1     Pending     0          2s
job2-5-nkkhx    0/1     Pending     0          2s
job2-6-5r76n    0/1     Pending     0          2s
job2-7-pmzb5    0/1     Pending     0          2s
job2-8-xdqtp    0/1     Pending     0          2s
job2-9-c4rcl    0/1     Pending     0          2s
```

Once `job1` completes, it frees the resources required by `job2` to run its pods
to make progress. Finally, all jobs complete.

#### Cleanup

Clean up the jobs by:

```shell
kubectl delete -f quick-job.yaml
kubectl delete -f /tmp/job1.yaml
kubectl delete -f /tmp/job2.yaml
```

## Drawbacks

When enabling `waitForPodsReady`, the admission of Workloads may
be unnecessarily slowed down by sequencing in case the cluster has enough
resources to support concurrent Workload startup.
