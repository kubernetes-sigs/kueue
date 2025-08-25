# MultiKueue with TAS and ProvisioningRequest on GKE

This example demonstrates how to set up and test MultiKueue with Topology-Aware Scheduling (TAS) and ProvisioningRequest on Google Kubernetes Engine (GKE). It includes configuration files and scripts to deploy MultiKueue across multiple GKE clusters and validate the integration.


# Setup and Usage

## Prerequisites
- [Google Cloud](https://cloud.google.com/) account set up.
- [gcloud](https://pypi.org/project/gcloud/) command line tool installed and configured to use your GCP project.
- [kubectl](https://kubernetes.io/docs/tasks/tools/) command line utility is installed.
- [terraform](https://developer.hashicorp.com/terraform/install) command line installed.

## Create Clusters

### Set Environment Variables

Before running Terraform commands, you need to set the `project_id` variable:

```bash
export TF_VAR_project_id="your-gcp-project-id"
```

### DWS Configuration

The Terraform configuration supports two scenarios for the worker cluster:

- **DWS Disabled (default)**: Worker nodes maintain 1 node minimum for immediate workload execution
- **DWS Enabled**: Worker nodes scale to zero and use Dynamic Workload Scheduler

To control DWS behavior, set the `enable_dws` variable:

```bash
# Enable DWS
export TF_VAR_enable_dws=true
```

### Run Terraform Commands

Once you've set the project_id, run the Terraform commands:

```
terraform -chdir=tf init
terraform -chdir=tf plan
terraform -chdir=tf apply
```

## Install Kueue

After creating the GKE clusters and updating your kubeconfig files, install the Kueue components by running:

```bash
./deploy-multikueue.sh
```

This script supports two optional environment flags:

- ${KUEUE_CUSTOM_INSTALL:-false}: If set to true, the script installs a custom version of Kueue from the container registry specified by the IMAGE_REGISTRY variable.

- ${KUEUE_BUILD_IMAGE:-false}: If set to true, the script builds and pushes the Kueue image to the registry defined in IMAGE_REGISTRY before installing it.

You can combine both flags if you want to build and install a custom Kueue version:

```bash
IMAGE_REGISTRY=gcr.io/your-project KUEUE_BUILD_IMAGE=true KUEUE_CUSTOM_INSTALL=true ./deploy-multikueue.sh
```

## Apply Setup

To configure your clusters for a specific feature, use the `manage-setup.sh` script with either `tas` or `tas-dws` as the feature name and `apply` or `delete` as the command:

```
./manage-setup.sh <tas|tas-dws> <apply|delete>
```

For example:
- To apply the setup: `./manage-setup.sh tas apply`
- To remove the setup: `./manage-setup.sh tas delete`

## Validate installation

Verify the Kueue installation and the connection between the manager and worker clusters:

```
kubectl get clusterqueues mgr-cluster-queue -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}CQ - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
kubectl get admissionchecks multikueue-ac -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}AC - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
kubectl get multikueuecluster multikueue-test-worker-us -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}MC-US - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
kubectl get multikueuecluster multikueue-test-worker-asia -o jsonpath="{range .status.conditions[?(@.type == \"Active\")]}MC-ASIA - Active: {@.status} Reason: {@.reason} Message: {@.message}{'\n'}{end}"
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
terraform -chdir=tf destroy
```


