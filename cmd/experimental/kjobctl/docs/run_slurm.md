# Run a Slurm Job on Kubernetes

This page demonstrates how to run a Slurm job on a Kubernetes cluster using Kjob.

## Before You Begin

Ensure the following prerequisites are met:

- A Kubernetes cluster is operational.
- The [kjobctl](installation.md) plugin is installed.

## Slurm Mode in Kjob

This mode is specifically designed to support Slurm, a highly scalable cluster management and job scheduling system. Although Kjob does not directly incorporate Slurm's components, it simulates Slurm behavior to facilitate a smooth transition for users. By translating Slurm concepts such as nodes into Kubernetes-compatible terms like pods, Kjob ensures that users familiar with Slurm will find the Kjob environment intuitive and easy to use. The addition of the Slurm mode to the ApplicationProfile in Kjob aims to replicate the experience of using the sbatch command in Slurm as closely as possible, thereby enhancing user satisfaction and efficiency in managing cluster jobs within a Kubernetes context.

### Supported Options

Kjob provides support for executing Slurm scripts by offering several options that can be specified either through the command line interface, `Kjobctl`, or via the `#SBATCH` directive embedded within the bash script. These options are detailed in the table below:

| Option              | Description |
|---------------------|-------------|
| -a, --array         | See [array option](https://slurm.schedmd.com/sbatch.html#OPT_array) for the specification. |
| --cpus-per-task     | Specifies how many CPUs a container inside a pod requires. |
| -e, --error         | Specifies where to redirect the standard error stream of a task. If not passed, it proceeds to stdout and is available via `kubectl logs`. |
| --gpus-per-task     | Specifies how many GPUs a container inside a pod requires. |
| -i, --input         | Specifies what to pipe into the script. |
| -J, --job-name=<jobname> | Specifies the job name. |
| --mem-per-cpu       | Specifies how much memory a container requires, multiplying the number of requested CPUs per task by mem-per-cpu. |
| --mem-per-gpu       | Specifies how much memory a container requires, multiplying the number of requested GPUs per task by mem-per-gpu. |
| --mem-per-task      | Specifies how much memory a container requires. |
| -N, --nodes         | Specifies the number of pods to be used at a time - parallelism in indexed jobs. |
| -n, --ntasks        | Specifies the number of identical containers inside of a pod, usually 1. |
| -o, --output        | Specifies where to redirect the standard output stream of a task. If not passed, it proceeds to stdout and is available via `kubectl logs`. |
| --partition         | Specifies the local queue name. |

If an unsupported flag is passed in the script, the command will fail with an error unless `--ignore-unknown-flags` is given.

### Supported Environment Variables

| Name                         | Description |
|------------------------------|-------------|
| $SLURM_JOB_ID                | The Job ID. |
| $SLURM_JOBID                 | Deprecated. Same as $SLURM_JOB_ID. |
| $SLURM_SUBMIT_DIR            | The path of the job submission directory. |
| $SLURM_SUBMIT_HOST           | The hostname of the node used for job submission. |
| $SLURM_JOB_NODELIST          | Contains the definition (list) of the nodes (actually pods) that is assigned to the job. To be supported later. |
| $SLURM_NODELIST              | Deprecated. Same as $SLURM_JOB_NODELIST. |
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

See [config/samples](../config/samples) for the full samples used in this section.

### 1. Create Slurm Template

First, you need to create a template for your job using the `JobTemplate` kind. This template should specify the container image with all the dependencies to run your task. Below is an example of a template to run a Python script.

```yaml
apiVersion: kjobctl.x-k8s.io/v1alpha1
kind: JobTemplate
metadata:
  name: sample-slurm-template
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
  name: sample-profile
  namespace: default
spec:
  supportedModes:
    - name: Slurm
      template: sample-slurm-template
      requiredFlags: []
```

Then, create the ApplicationProfile by running:

```bash
kubectl create -f config/samples/application_profile.yaml
```

### 2. Load the Python Script

Now that you have created your `JobTemplate` for Slurm, you can make your script available to the pods by mounting a `VolumeBundle`. In the following example, a `ConfigMap` is used to pass the script to be executed.

```yaml 
---
apiVersion: kjobctl.x-k8s.io/v1alpha1
kind: VolumeBundle
metadata:
  name: slurm-volume-bundle
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
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: slurm-code-sample
data:
  sample_code.py: |
    import time

    print('Start at ' + time.strftime('%H:%M:%S'))

    print('Sleep for 10 seconds...')
    time.sleep(10)

    print('Stop at ' + time.strftime('%H:%M:%S'))
```

To create the `VolumeBundle`, run:

```bash
kubectl create -f config/samples/slurm_volume_bundle.yaml
```

### 3. Submit Job

After setting up the `ApplicationProfile` and `VolumeBundle`, you're ready to run tasks using your template. To submit a job, you need to pass a batch script to the `create slurm` command. You can run your script with custom configurations by the supported options via the `#SBATCH` directive or flags.

Below is an example of a simple batch script which wraps the Python script loaded in the previous step and executes it. Additionally, it sets the array option to run 3 multiple jobs and the maximum number of simultaneously running tasks to 2.

```bash
#!/bin/bash

#SBATCH --array=1-3%2

echo "Now processing task ID: ${SLURM_ARRAY_TASK_ID}"
python /home/slurm/samples/sample_code.py

exit 0
```

To run this batch script, execute:

```bash
kubectl-kjob create slurm --profile sample-profile --config config/samples/slurm-sample.sh
```

Now, after submitting the job, you should be able to see something similar to the following output in the logs:

```
Now processing task ID: 1
Start at 17:08:04
Sleep for 10 seconds...
Stop at 17:08:14
```
