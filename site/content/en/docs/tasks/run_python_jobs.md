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

```python
{{% include "python/install-kueue-queues.py" %}}
```

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
python install-kueue-queues.py --version 0.4.0
```

### Sample Job

For the next example, let's start with a cluster with Kueue installed, and first create our queues:

```python
{{% include "python/sample-job.py" %}}
```

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
