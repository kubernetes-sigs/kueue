# Multikueue-tas-dws-integration

This repository contains files to test MultiKueue with TAS and DWS on GKE. It provides resources and scripts for deploying and validating MultiKueue integration with both the Task Assignment Scheduler (TAS) and Dynamic Workload Scheduler (DWS) across multiple GKE clusters in different regions.


# Setup and Usage

## Prerequisites
- [Google Cloud](https://cloud.google.com/) account set up.
- [gcloud](https://pypi.org/project/gcloud/) command line tool installed and configured to use your GCP project.
- [kubectl](https://kubernetes.io/docs/tasks/tools/) command line utility is installed.
- [terraform](https://developer.hashicorp.com/terraform/install) command line installed.

## Create Clusters

```
terraform -chdir=tf init
terraform -chdir=tf plan
terraform -chdir=tf apply -var project_id=<YOUR PROJECT ID>
```

## Install Kueue

After creating the GKE clusters and updating your kubeconfig files, install the Kueue components:

```
./deploy-multikueue.sh  
```

## Apply Setup

To configure your clusters for a specific feature, use the `apply-setup.sh` script with either `tas` or `tas-dws` as the argument:

```
./apply-setup.sh  <tas|tas-dws>
```

## Validate installation

Verify the Kueue installation and the connection between the manager and worker clusters:

```
kubectl get clusterqueues dws-cluster-queue -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}CQ - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
kubectl get admissionchecks multikueue-ac -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}AC - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
kubectl get multikueuecluster multikueue-test-worker-us -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}MC-US - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
```

A successful output should look like this:

```
CQ - Active: True Reason: Ready Message: Can admit new workloads
AC - Active: True Reason: Active Message: The admission check is active
MC-ASIA - Active: True Reason: Active Message: Connected
MC-US - Active: True Reason: Active Message: Connected
MC-EU - Active: True Reason: Active Message: Connected
```

## Launch job

Submit your job to the Kueue controller, which will run it on a worker cluster with available resources:

```
kubectl create -f job.yaml
```

## Get the status of the job

To check the job status and see where it's scheduled:

```
kubectl get workloads.kueue.x-k8s.io -o jsonpath='{range .items[*]}{.status.admissionChecks}{"\n"}{end}'
```

In the output message, you can find where the job is scheduled#

## Destroy resources


```
terraform -chdir=tf destroy -var project_id=<YOUR PROJECT ID>
```


