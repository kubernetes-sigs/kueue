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
#!/usr/bin/env python3

from kubernetes import utils, config, client
import tempfile
import requests
import argparse

# install-kueue-queues.py will:
# 1. install queue from the latest on GitHub
# This example will demonstrate installing Kueue and applying a YAML file (local) to install Kueue

# Make sure your cluster is running!
config.load_kube_config()
crd_api = client.CustomObjectsApi()
api_client = crd_api.api_client


def get_parser():
    parser = argparse.ArgumentParser(
        description="Submit Kueue Job Example",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument(
        "--version",
        help="Version of Kueue to install",
        default="0.3.2",
    )
    return parser


def main():
    """
    Install Kueue and the Queue components.

    This will error if they are already installed.
    """
    parser = get_parser()
    args, _ = parser.parse_known_args()
    install_kueue(args.version)


def install_kueue(version):
    """
    Install Kueue of a particular version.
    """
    print("⭐️ Installing Kueue...")
    url = f"https://github.com/kubernetes-sigs/kueue/releases/download/v{version}/manifests.yaml"
    with tempfile.NamedTemporaryFile(delete=True) as install_yaml:
        res = requests.get(url)
        assert res.status_code == 200
        install_yaml.write(res.content)
        utils.create_from_yaml(api_client, install_yaml.name)


if __name__ == "__main__":
    main()
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
python install-kueue-queues.py --version 0.3.2
```

### Sample Job

For the next example, let's start with a cluster with Kueue installed, and first create our queues:

```bash
kubectl apply -f ../single-clusterqueue-setup.yaml 
```
```console
resourceflavor.kueue.x-k8s.io/default-flavor created
clusterqueue.kueue.x-k8s.io/cluster-queue created
localqueue.kueue.x-k8s.io/user-queue created
```

And now launch a job. This simple example mimics the [sample-job.yaml](https://github.com/kubernetes-sigs/kueue/blob/main/examples/sample-job.yaml) and demonstrates submitting a job and then listing it in the Kueue queue.
Write this to `sample-job.py`:

```python
#!/usr/bin/env python3

import argparse
from kubernetes import config, client

# create_job.py
# This example will demonstrate full steps to submit a Job.

# Make sure your cluster is running!
config.load_kube_config()
crd_api = client.CustomObjectsApi()
api_client = crd_api.api_client


def get_parser():
    parser = argparse.ArgumentParser(
        description="Submit Kueue Job Example",
        formatter_class=argparse.RawTextHelpFormatter,
    )
    parser.add_argument(
        "--job-name",
        help="generateName field to set for job",
        default="sample-job-",
    )
    parser.add_argument(
        "--image",
        help="container image to use",
        default="gcr.io/k8s-staging-perf-tests/sleep:v0.0.3",
    )
    parser.add_argument(
        "--args",
        nargs="+",
        help="args for container",
        default=["30s"],
    )
    return parser


def generate_job_crd(job_name, image, args):
    """
    Generate an equivalent job CRD to sample-job.yaml
    """
    metadata = client.V1ObjectMeta(
        generate_name=job_name, labels={"kueue.x-k8s.io/queue-name": "user-queue"}
    )

    # Job container
    container = client.V1Container(
        image=image,
        name="dummy-job",
        args=args,
        resources={
            "requests": {
                "cpu": 1,
                "memory": "200Mi",
            }
        },
    )

    # Job template
    template = {"spec": {"containers": [container], "restartPolicy": "Never"}}
    return client.V1Job(
        api_version="batch/v1",
        kind="Job",
        metadata=metadata,
        spec=client.V1JobSpec(
            parallelism=1, completions=3, suspend=True, template=template
        ),
    )


def main():
    """
    Run a job.
    """
    parser = get_parser()
    args, _ = parser.parse_known_args()

    # Generate a CRD spec
    crd = generate_job_crd(args.job_name, args.image, args.args)
    batch_api = client.BatchV1Api()
    print(f"📦️ Container image selected is {args.image}...")
    print(f"⭐️ Creating sample job with prefix {args.job_name}...")
    batch_api.create_namespaced_job("default", crd)
    print(
        'Use:\n"kubectl get queue" to see queue assignment\n"kubectl get jobs" to see jobs'
    )


if __name__ == "__main__":
    main()
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
