---
title: "Run Jobs Using Python"
linkTitle: "Python"
date: 2023-07-05
weight: 7
description: >
  Run Kueue jobs programmatically with Python
---

This guide is for [batch users](/v0.19/docs/tasks#batch-user) that have a basic understanding of interacting with Kubernetes from Python. For more information, see [Kueue's overview](/v0.19/docs/overview).

## Before you begin

Check [administer cluster quotas](/v0.19/docs/tasks/manage/administer_cluster_quotas) for details on the initial cluster setup.
You'll also need [uv](https://docs.astral.sh/uv/) installed. Each example script includes
[inline script metadata (PEP 723)](https://peps.python.org/pep-0723/) so `uv run` will
automatically create an isolated environment and install the required dependencies.

```bash
# Install uv (if not already installed)
curl -LsSf https://astral.sh/uv/install.sh | sh
```

No manual `pip install` or virtual environment setup is needed — just run the scripts
with `uv run`.

You can either follow the [install instructions](https://github.com/kubernetes-sigs/kueue#installation) for Kueue, or use the install example, below.

## Kueue in Python

Kueue at the core is a controller for a [Custom Resource](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/), and so to interact with it from Python we don't need a custom SDK, but rather we can use the generic functions provided by the
[Kubernetes Python](https://github.com/kubernetes-client/python) library. In this guide, we provide several examples
for interacting with Kueue in this fashion. If you would like to request a new example or would like help for a specific use
case, please [open an issue](https://github.com/kubernetes-sigs/kueue/issues).

## Examples

The following examples demonstrate different use cases for using Kueue in Python.

### Install Kueue

This example demonstrates installing Kueue to an existing cluster. You can save this
script to your local machine as `install-kueue-queues.py`. 

{{< include file="v0.19/examples/python/install-kueue-queues.py" lang="python" >}}

And then run as follows:

```bash
uv run install-kueue-queues.py 
```

```console
⭐️ Installing Kueue...
⭐️ Applying queues from single-clusterqueue-setup.yaml...
```

You can also target a specific version:

```bash
uv run install-kueue-queues.py --version {{< param "version" >}}
```

### Sample Job

For the next example, let's start with a cluster with Kueue installed, and first create our queues:

{{< include file="v0.19/examples/python/sample-job.py" code="true" lang="python" >}}

And run as follows:

```bash
uv run sample-job.py
```
```console
📦️ Container image selected is registry.k8s.io/e2e-test-images/agnhost:2.53...
⭐️ Creating sample job with prefix sample-job-...
Use:
"kubectl get queue" to see queue assignment
"kubectl get jobs" to see jobs
```

or try changing the name (`generateName`) of the job:

```bash
uv run sample-job.py --job-name sleep-job-
```

```console
📦️ Container image selected is registry.k8s.io/e2e-test-images/agnhost:2.53...
⭐️ Creating sample job with prefix sleep-job-...
Use:
"kubectl get queue" to see queue assignment
"kubectl get jobs" to see jobs
```

You can also change the container image with `--image` and args with `--args`.
For more customization, you can edit the example script.

### Interact with Queues and Jobs

If you are developing an application that submits jobs and needs to interact
with and check on them, you likely want to interact with queues or jobs directly.
After running the example above, you can test the following example to interact
with the results. Write the following to a script called `sample-queue-control.py`.

{{< include file="v0.19/examples/python/sample-queue-control.py" lang="python" >}}

To make the output more interesting, we can run a few random jobs first:

```bash
uv run sample-job.py
uv run sample-job.py
uv run sample-job.py --job-name tacos
```

And then run the script to see your queue and sample job that you submit previously.

```bash
uv run sample-queue-control.py
```
```console
⛑️  Local Queues
Found queue user-queue
  Admitted workloads: 3
  Pending workloads: 0
  Flavor default-flavor has resources [{'name': 'cpu', 'total': '3'}, {'name': 'memory', 'total': '600Mi'}]

💼️ Jobs
Found job sample-job-8n5sb
  Succeeded: 3
  Ready: 0
Found job sample-job-gnxtl
  Succeeded: 1
  Ready: 0
Found job tacos46bqw
  Succeeded: 1
  Ready: 1
```

If you wanted to filter jobs to a specific queue, you can do this via the job labels
under `job["metadata"]["labels"]["kueue.x-k8s.io/queue-name"]'. To list a specific job by
name, you can do:

```python
from kubernetes import client, config

# Interact with batch
config.load_kube_config()
batch_api = client.BatchV1Api()

# This is providing the name, and namespace
job = batch_api.read_namespaced_job("tacos46bqw", "default")
print(job)
```

See the [BatchV1](https://github.com/kubernetes-client/python/blob/master/kubernetes/docs/BatchV1Api.md)
API documentation for more calls.


### Flux Operator Job

For this example, we will be using the [Flux Operator](https://github.com/flux-framework/flux-operator)
to submit a job, and specifically using the [Python SDK](https://github.com/flux-framework/flux-operator/tree/main/sdk/python/v1alpha1) to do this easily. Given our Python environment created in the [setup](#before-you-begin), we can install this Python SDK directly to it as follows:

The `fluxoperator` dependency is declared in the script's inline metadata and will be
automatically installed by `uv run`.

We will also need to [install the Flux operator](https://flux-framework.org/flux-operator/getting_started/user-guide.html#quick-install). 

```bash
kubectl apply -f https://raw.githubusercontent.com/flux-framework/flux-operator/main/examples/dist/flux-operator.yaml
```

Write the following script to `sample-flux-operator-job.py`:

{{< include file="v0.19/examples/python/sample-flux-operator-job.py" lang="python" >}}

Now try running the example:

```bash
uv run sample-flux-operator-job.py
```
```console
📦️ Container image selected is ghcr.io/flux-framework/flux-restful-api...
⭐️ Creating sample job with prefix hello-world...
Use:
"kubectl get queue" to see queue assignment
"kubectl get pods" to see pods
```

You'll be able to almost immediately see the MiniCluster job admitted to the local queue:

```bash
kubectl get queue
```
```console
NAME         CLUSTERQUEUE    PENDING WORKLOADS   ADMITTED WORKLOADS
user-queue   cluster-queue   0                   1
```

And the 4 pods running (we are creating a networked cluster with 4 nodes):

```bash
kubectl get pods
```
```console
NAME                       READY   STATUS      RESTARTS   AGE
hello-world7qgqd-0-wp596   1/1     Running     0          7s
hello-world7qgqd-1-d7r87   1/1     Running     0          7s
hello-world7qgqd-2-rfn4t   1/1     Running     0          7s
hello-world7qgqd-3-blvtn   1/1     Running     0          7s
```

If you look at logs of the main broker pod (index 0 of the job above), there is a lot of
output for debugging, and you can see "hello world" running at the end:

```bash
kubectl logs hello-world7qgqd-0-wp596 
```

<details>

<summary>Flux Operator Lead Broker Output</summary>

```console
🌀 Submit Mode: flux start -o --config /etc/flux/config -Scron.directory=/etc/flux/system/cron.d   -Stbon.fanout=256   -Srundir=/run/flux    -Sstatedir=/var/lib/flux   -Slocal-uri=local:///run/flux/local     -Slog-stderr-level=6    -Slog-stderr-mode=local  flux submit  -n 1 --quiet  --watch echo hello world
broker.info[0]: start: none->join 0.399725ms
broker.info[0]: parent-none: join->init 0.030894ms
cron.info[0]: synchronizing cron tasks to event heartbeat.pulse
job-manager.info[0]: restart: 0 jobs
job-manager.info[0]: restart: 0 running jobs
job-manager.info[0]: restart: checkpoint.job-manager not found
broker.info[0]: rc1.0: running /etc/flux/rc1.d/01-sched-fluxion
sched-fluxion-resource.info[0]: version 0.27.0-15-gc90fbcc2
sched-fluxion-resource.warning[0]: create_reader: allowlist unsupported
sched-fluxion-resource.info[0]: populate_resource_db: loaded resources from core's resource.acquire
sched-fluxion-qmanager.info[0]: version 0.27.0-15-gc90fbcc2
broker.info[0]: rc1.0: running /etc/flux/rc1.d/02-cron
broker.info[0]: rc1.0: /etc/flux/rc1 Exited (rc=0) 0.5s
broker.info[0]: rc1-success: init->quorum 0.485239s
broker.info[0]: online: hello-world7qgqd-0 (ranks 0)
broker.info[0]: online: hello-world7qgqd-[0-3] (ranks 0-3)
broker.info[0]: quorum-full: quorum->run 0.354587s
hello world
broker.info[0]: rc2.0: flux submit -n 1 --quiet --watch echo hello world Exited (rc=0) 0.3s
broker.info[0]: rc2-success: run->cleanup 0.308392s
broker.info[0]: cleanup.0: flux queue stop --quiet --all --nocheckpoint Exited (rc=0) 0.1s
broker.info[0]: cleanup.1: flux cancel --user=all --quiet --states RUN Exited (rc=0) 0.1s
broker.info[0]: cleanup.2: flux queue idle --quiet Exited (rc=0) 0.1s
broker.info[0]: cleanup-success: cleanup->shutdown 0.252899s
broker.info[0]: children-complete: shutdown->finalize 47.6699ms
broker.info[0]: rc3.0: running /etc/flux/rc3.d/01-sched-fluxion
broker.info[0]: rc3.0: /etc/flux/rc3 Exited (rc=0) 0.2s
broker.info[0]: rc3-success: finalize->goodbye 0.212425s
broker.info[0]: goodbye: goodbye->exit 0.06917ms
```

</details>

If you submit and ask for four tasks, you'll see "hello world" four times:

```bash
uv run sample-flux-operator-job.py --tasks 4
```
```console
...
broker.info[0]: quorum-full: quorum->run 23.5812s
hello world
hello world
hello world
hello world
```

You can further customize the job, and can ask questions on the [Flux Operator issues board](https://github.com/flux-framework/flux-operator/issues).
Finally, for instructions for how to do this with YAML outside of Python, see [Run A Flux MiniCluster](/v0.19/docs/tasks/run/external_workloads/flux_miniclusters/).

### Kubeflow Trainer Job

For this example, we will use [Kubeflow Trainer](https://trainer.kubeflow.org/en/latest/)
to submit a `TrainJob` (the Kubeflow Trainer v2 API), and specifically the
[Kubeflow SDK](https://github.com/kubeflow/sdk) to do this easily. Unlike the previous
examples, the SDK builds and submits the resource for us, so we don't construct the CRD by
hand. The `kubeflow` dependency is declared in the script's inline metadata and will be
automatically installed by `uv run`.

This example assumes Kubeflow Trainer v2 is installed, the built-in `torch-distributed`
`ClusterTrainingRuntime` is available, and Kueue is configured to manage TrainJobs. For these
prerequisites (installing Kubeflow Trainer, the runtime, and enabling the TrainJob
integration in Kueue), follow the [Run A TrainJob](/v0.19/docs/tasks/run/trainjobs/) guide. You'll
also need the queues created:

```bash
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/kueue/main/site/static/examples/admin/single-clusterqueue-setup.yaml
```

Write the following script to `sample-trainjob.py`:

{{< include file="v0.19/examples/python/sample-trainjob.py" lang="python" >}}

The Kueue integration is the `Labels` option, which sets the `kueue.x-k8s.io/queue-name`
label on the TrainJob so Kueue can admit it to the right local queue (the same label the
other examples set on their job metadata).

Now try running the example:

```bash
uv run sample-trainjob.py
```
```console
⭐️ Submitting TrainJob to runtime torch-distributed...
⭐️ Created TrainJob md3b82e943a5
Use:
"kubectl get queue" to see queue assignment
"kubectl get trainjobs" to see TrainJobs
```

Using the TrainJob name printed above, you can watch the TrainJob and stream its logs with
the Kubeflow SDK:

```python
from kubeflow.trainer import TrainerClient

client = TrainerClient()
job_name = "md3b82e943a5"

# Wait until Kueue admits the TrainJob and it starts running, then stream its logs.
client.wait_for_job_status(name=job_name, status={"Running"})
for logline in client.get_job_logs(job_name, follow=True):
    print(logline)
```
```console
[Gloo] Rank 0 is connected to 0 peer ranks. Expected number of connected peer ranks is : 0
Distributed Training with WORLD_SIZE: 1, RANK: 0, LOCAL_RANK: 0.
Train Epoch: 0 [0/60000]	Loss: 2.310645
Train Epoch: 0 [1000/60000]	Loss: 1.935180
Train Epoch: 0 [2000/60000]	Loss: 2.096165
...
Train Epoch: 2 [58000/60000]	Loss: 0.372064
Train Epoch: 2 [59000/60000]	Loss: 0.226857
Training is finished
```

You can further customize the job, and ask questions on the
[Kubeflow Trainer issues board](https://github.com/kubeflow/trainer/issues).
Finally, for instructions for how to do this with YAML outside of Python, see
[Run A TrainJob](/v0.19/docs/tasks/run/trainjobs/).
