# Run a Slurm Job on Kubernetes

This page demonstrates how to run a Slurm job on a Kubernetes cluster using Kjob.

## Before You Begin

Ensure the following prerequisites are met:

- A Kubernetes cluster is operational.
- The [kjobctl](installation.md) plugin is installed.

## Slurm Mode in Kjob

This mode is specifically designed to offer a compatibility mode for [Slurm](https://slurm.schedmd.com/) jobs. Kjob does not directly incorporate Slurm's components.

Instead, it simulates Slurm behavior to facilitate a smooth transition for users.

By translating Slurm concepts such as nodes into Kubernetes-compatible terms like pods, Kjob ensures that users familiar with Slurm will find the Kjob environment intuitive and easy to use. 

The addition of the Slurm mode to the ApplicationProfile in Kjob aims to replicate the experience of using the sbatch command in Slurm as closely as possible, thereby enhancing user satisfaction and efficiency in managing cluster jobs within a Kubernetes context.

### Supported Options

Kjob provides support for executing Slurm scripts by offering several options that can be specified either through the command line interface, `Kjobctl`, or via the `#SBATCH` directive embedded within the bash script. These options are detailed in the table below:

| Option              | Description |
|---------------------|-------------|
| -a, --array         | See [array option](https://slurm.schedmd.com/sbatch.html#OPT_array) for the specification. |
| -c, --cpus-per-task | Specifies how many CPUs a container inside a pod requires. |
| -e, --error         | Specifies where to redirect the standard error stream of a task. If not passed, it proceeds to stdout and is available via `kubectl logs`. |
| --gpus-per-task     | Specifies how many GPUs a container inside a pod requires. |
| -i, --input         | Specifies what to pipe into the script. |
| -J, --job-name=<jobname> | Specifies the job name. |
| --mem               | Specifies how much memory a pod requires. |
| --mem-per-cpu       | Specifies how much memory a container requires, multiplying the number of requested CPUs per task by mem-per-cpu. |
| --mem-per-gpu       | Specifies how much memory a container requires, multiplying the number of requested GPUs per task by mem-per-gpu. |
| --mem-per-task      | Specifies how much memory a container requires. |
| -N, --nodes         | Specifies the number of pods to be used at a time - parallelism in indexed jobs. |
| -n, --ntasks        | Specifies the number of identical containers inside of a pod, usually 1. |
| -o, --output        | Specifies where to redirect the standard output stream of a task. If not passed, it proceeds to stdout and is available via `kubectl logs`. |
| --partition         | Specifies the local queue name. See [Local Queue](https://kueue.sigs.k8s.io/docs/concepts/local_queue/) for more information. |
| -D, --chdir         | Change directory before executing the script. |
| -t, --time          | Set a limit on the total run time of the job. A time limit of zero requests that no time limit be imposed. Acceptable time formats include "minutes", "minutes:seconds", "hours:minutes:seconds", "days-hours", "days-hours:minutes" and "days-hours:minutes:seconds". |

If an unsupported flag is passed in the script, the command will fail with an error unless `--ignore-unknown-flags` is given.

### Supported Input Environment Variables

> NOTE: Environment variables will override any options set in a batch script, and command line 
> options will override any environment variables.

| Name                  | Description             |
|-----------------------|-------------------------|
| $SBATCH_ARRAY_INX     | Same as -a, --array     |
| $SBATCH_GPUS_PER_TASK | Same as --gpus-per-task |
| $SBATCH_MEM_PER_NODE  | Same as --mem           |
| $SBATCH_MEM_PER_CPU   | Same as --mem-per-cpu   |
| $SBATCH_MEM_PER_GPU   | Same as --mem-per-gpu   |
| $SBATCH_OUTPUT        | Same as -o, --output    |
| $SBATCH_ERROR         | Same as -e, --error     |
| $SBATCH_INPUT         | Same as -i, --input     |
| $SBATCH_JOB_NAME      | Same as -J, --job-name  |
| $SBATCH_PARTITION     | Same as -p, --partition |
| $SBATCH_TIMELIMIT     | Same as -t, --time      |

### Supported Output Environment Variables

| Name                         | Description |
|------------------------------|-------------|
| $SLURM_ARRAY_TASK_ID         | Job array ID (index) number. |
| $SLURM_JOB_ID                | The Job ID. |
| $SLURM_JOBID                 | Deprecated. Same as $SLURM_JOB_ID. |
| $SLURM_SUBMIT_DIR            | The path of the job submission directory. |
| $SLURM_SUBMIT_HOST           | The hostname of the node used for job submission. |
| $SLURM_JOB_NODELIST          | Contains the definition (list) of the nodes (actually pods) that is assigned to the job. |
| $SLURM_JOB_FIRST_NODE        | First element of  SLURM_JOB_NODELIST. |
| $SLURM_JOB_FIRST_NODE_IP     | IP of the first element, obtained via nslookup. |
| $SLURM_CPUS_PER_TASK         | Number of CPUs per task. |
| $SLURM_CPUS_ON_NODE          | Number of CPUs on the allocated node (actually pod). |
| $SLURM_JOB_CPUS_PER_NODE     | Count of processors available to the job on this node. |
| $SLURM_CPUS_PER_GPU          | Number of CPUs requested per allocated GPU. |
| $SLURM_MEM_PER_CPU           | Memory per CPU. Same as --mem-per-cpu. |
| $SLURM_MEM_PER_GPU           | Memory per GPU. |
| $SLURM_MEM_PER_NODE          | Memory per node. Same as --mem. |
| $SLURM_GPUS                  | Number of GPUs requested (in total). |
| $SLURM_NTASKS                | Same as -n, --ntasks. The number of tasks. |
| $SLURM_NTASKS_PER_NODE       | Number of tasks requested per node. |
| $SLURM_NTASKS_PER_SOCKET     | Number of tasks requested per socket. To be supported later. |
| $SLURM_NTASKS_PER_CORE       | Number of tasks requested per core. To be supported later. |
| $SLURM_NTASKS_PER_GPU        | Number of tasks requested per GPU. To be supported later. |
| $SLURM_NPROCS                | Same as -n, --ntasks. See $SLURM_NTASKS. |
| $SLURM_NNODES                | Total number of nodes (actually pods) in the job’s resource allocation. |
| $SLURM_TASKS_PER_NODE        | Number of tasks to be initiated on each node. |
| $SLURM_ARRAY_JOB_ID          | Job array’s master job ID number. For now, same as $SLURM_JOB_ID. |
| $SLURM_ARRAY_TASK_COUNT      | Total number of tasks in a job array. |
| $SLURM_ARRAY_TASK_MAX        | Job array’s maximum ID (index) number. |
| $SLURM_ARRAY_TASK_MIN        | Job array’s minimum ID (index) number. |

## Example

The following example demonstrates a use case for running a Python script.

See [config/samples/slurm](../config/samples/slurm/) for the full samples used in this section.

### 1. Create Slurm Template

First, you need to create a template for your job using the `JobTemplate` kind. This template should specify the container image with all the dependencies to run your task. Below is an example of a template to run a Python script.

```yaml
apiVersion: kjobctl.x-k8s.io/v1alpha1
kind: JobTemplate
metadata:
  name: slurm-template
  namespace: default
template:
  spec:
    parallelism: 3
    completions: 3
    completionMode: Indexed
    template:
      spec:
        containers:
          - name: sample-container
            image: python:3-slim
        restartPolicy: OnFailure
```

Once you have created your template, you need an `ApplicationProfile` containing `Slurm` and your new template in the supported modes as shown below:

```yaml
apiVersion: kjobctl.x-k8s.io/v1alpha1
kind: ApplicationProfile
metadata:
  name: slurm-profile
  namespace: default
spec:
  supportedModes:
    - name: Slurm
      template: slurm-template
  volumeBundles: ["slurm-volume-bundle"]
```

Then, save the file as `application_profile.yaml` and create the ApplicationProfile by running:

```bash
kubectl create -f application_profile.yaml
```

> Note: This setup process, including the creation of the JobTemplate and ApplicationProfile, only needs to be completed once and can be applied to subsequent Python script executions.

### 2. Prepare the Python Script for the Job

Before loading the Python script for use with the pods, it's crucial to consider how the script will be made accessible. 

For the purposes of this tutorial, we'll utilize a ConfigMap to store and pass the Python script. While this method simplifies the setup and is suitable for demonstration purposes, for a scalable setup, using NFS or a similar shared file system is necessary.

#### Creating a ConfigMap with Your Python Script

Create a file named `sample_code.py` and add your Python script. For example:

```python
import time

print('Start at ' + time.strftime('%H:%M:%S'))

print('Sleep for 10 seconds...')
time.sleep(10)

print('Stop at ' + time.strftime('%H:%M:%S'))
```

To create a `ConfigMap` from this Python script, save it and then run the following command:

```bash
kubectl create configmap slurm-code-sample --from-file=sample_code.py
```

This command packages your script into a `ConfigMap` named `slurm-code-sample`.

### 3. Integrate the Script with a VolumeBundle

With your Python script now encapsulated in a `ConfigMap`, the next step is to make it accessible to your Slurm pods through a `VolumeBundle`.

#### Creating a VolumeBundle

Define a `VolumeBundle` object to specify how the containers should mount the volumes containing your script:

```yaml
apiVersion: kjobctl.x-k8s.io/v1alpha1
kind: VolumeBundle
metadata:
  name: slurm-volume-bundle
  namespace: default
spec:
  volumes:
    - name: slurm-code-sample
      configMap:
        name: slurm-code-sample
        items:
          - key: sample_code.py
            path: sample_code.py
  containerVolumeMounts:
    - name: slurm-code-sample
      mountPath: /home/slurm/samples
  envVars:
    - name: ENTRYPOINT_PATH
      value: /home/slurm/samples
```

Save this definition as a file named `slurm_volume_bundle.yaml`. And then, apply this configuration to your Kubernetes cluster by running:

```bash
kubectl create -f slurm_volume_bundle.yaml
```

This setup mounts the `ConfigMap` as a volume at `/home/slurm/samples` on the pods, making your Python script readily accessible to be executed as part of your Slurm job.

### 4. Submit Job

After setting up the `ApplicationProfile` and `VolumeBundle`, you're ready to run tasks using your template. To submit a job, you need to pass a batch script to the `create slurm` command. You can run your script with custom configurations by the supported options via the `#SBATCH` directive or flags.

Below is an example of a simple batch script which wraps the Python script loaded in the previous step and executes it. Additionally, it sets the array option to run 3 multiple jobs and the maximum number of simultaneously running tasks to 2.

```bash
#!/bin/bash

#SBATCH --array=1-3%2

echo "Now processing task ID: ${SLURM_ARRAY_TASK_ID}"
python /home/slurm/samples/sample_code.py
```

To run this batch script, save it as `slurm-sample.sh` and execute:

```bash
kubectl-kjob create slurm --profile slurm-profile -- slurm-sample.sh
```

Now, after submitting the job, you should be able to see something similar to the following output in the logs:

```
Now processing task ID: 1
Start at 17:08:04
Sleep for 10 seconds...
Stop at 17:08:14
```
