---
title: "Run Jobs Using Python"
date: 2023-07-05
weight: 7
description: >
  Run Kueue jobs programmatically with Python
---

This guide is for [batch users](/docs/tasks#batch-user) that have a basic understanding of interacting with Kubernetes from Python. For more information, see [Kueue's overview](/docs/overview).

## Before you begin

Check [administer cluster quotas](/docs/tasks/administer_cluster_quotas) for details on the initial cluster setup.
You'll also need kubernetes python installed. We recommend a virtual environment.

```bash
python -m venv env
source env/bin/activate
pip install kubernetes requests
```

Note that the following versions were used for developing these examples:

 - **Python**: 3.9.12
 - **kubernetes**: 26.1.0
 - **requests**: 2.31.0

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

{{% include "python/install-kueue-queues.py" "python" %}}

And then run as follows:

```bash
python install-kueue-queues.py 
```

```console
⭐️ Installing Kueue...
⭐️ Applying queues from single-clusterqueue-setup.yaml...
```

You can also target a specific version:

```bash
python install-kueue-queues.py --version {{< param "version" >}}
```

### Sample Job

For the next example, let's start with a cluster with Kueue installed, and first create our queues:

{{% include "python/sample-job.py" "python" %}}


And run as follows:

```bash
python sample-job.py
```
```console
📦️ Container image selected is gcr.io/k8s-staging-perf-tests/sleep:v0.0.3...
⭐️ Creating sample job with prefix sample-job-...
Use:
"kubectl get queue" to see queue assignment
"kubectl get jobs" to see jobs
```

or try changing the name (`generateName`) of the job:

```bash
python sample-job.py --job-name sleep-job-
```

```console
📦️ Container image selected is gcr.io/k8s-staging-perf-tests/sleep:v0.0.3...
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

{{% include "python/install-kueue-queues.py" "python" %}}

To make the output more interesting, we can run a few random jobs first:

```bash
python sample-job.py
python sample-job.py
python sample-job.py --job-name tacos
```

And then run the script to see your queue and sample job that you submit previously.

```bash
python sample-queue-control.py
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

