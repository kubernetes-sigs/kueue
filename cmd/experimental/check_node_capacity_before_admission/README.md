# Kueue Admission Check Controller with Resource Monitoring

This project is a Kueue Admission Check Controller integrated with resource monitoring capabilities. It leverages [Kueue](https://github.com/kubernetes-sigs/kueue) to manage workloads and includes functionality to log the current cluster resource snapshot whenever a workload is admitted. 

Generally an Admission Check Controller in Kubernetes intercepts and validates incoming workloads before they are admitted to the cluster, applying custom logic to enforce policies or constraints. In this project, the ACC doesn't enforce any policy and the Resource Monitor runs independently but provides real-time resource data, which the controller logs when workloads are admitted, enhancing visibility into cluster resources.

Based on https://github.com/kubernetes-sigs/kueue/pull/3265

## Features

- **Admission Control**: If a workload falls under the Guaranteed QoS then only accept it if there are nodes on the cluster which can satisfy the workload resources request. Workloads with other QoS are always admitted.
- **Resource Monitoring**: Periodically collects node and pod resource usage and logs snapshots upon workload admission.
- **RBAC Configurations**: Includes necessary permissions to interact with Kubernetes API resources like nodes, pods, workloads, and admission checks. 

---

## Prerequisites

1. **Kubernetes Cluster**: A running Kubernetes cluster (e.g., `k3d`).
2. **Docker**: To build the container image.
3. **kubectl**: To interact with the cluster.
4. **k3d**: For local development and testing.

---

## Build and Deploy


### 1. **Build and Push the Docker Image** 

Build and push the container image for the Admission Check Controller:

```bash
IMG=$your-registry/acc-3211:latest make doccker-build
IMG=$your-registry/acc-3211:latest make doccker-push
```

### 2. **Install and Configure Kueue**

Install Kueue: https://github.com/kubernetes-sigs/kueue?tab=readme-ov-file#installation

---

Deploy the AdmissionCheckController in the `kueue-system` namespace.

1. Update the Deployment in [config/manager/manager.yaml](config/manager/manager.yaml) to reference your image and any imagePullSecret.
2. Create the RBAC objects in [](config/rbac):

```commandline
kubectl apply -f config/rbac/role.yaml
kubectl apply -f config/rbac/role_binding.yaml
kubectl apply -f config/rbac/leader_election_role.yaml
kubectl apply -f config/rbac/leader_election_role_binding.yaml
```

### 3. Final Setup of Cluster Queue and Admission Check

#### 3.2 **Set Up a Single Cluster Queue Environment**

Set up the Kueue environment with a single cluster queue:

```bash
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/single-clusterqueue-setup.yaml
```

#### 3.3 **Create an AdmissionCheck Managed by `check-node-capacity-before-admission`**

Create the AdmissionCheck object: [config/manager/admission_check.yaml](config/manager/admission_check.yaml).

```bash
kubectl apply -f config/manager/admission_check.yaml
```

Verify that the AdmissionCheck is marked as `Running`:

```bash
kubectl get admissionchecks.kueue.x-k8s.io check-node-capacity-before-admission -o=jsonpath='{.status.conditions[?(@.type=="Active")].status}{" -> "}{.status.conditions[?(@.type=="Active")].message}{"\n"}'
```

Expected output:
```plaintext
True -> experimental.kueue.x-k8s.io/check-node-capacity-before-admission is running
```

#### 3.4 **Add the Admission Check to the Cluster Queue**

Patch the cluster queue to include the AdmissionCheck:

```bash
kubectl patch clusterqueues.kueue.x-k8s.io cluster-queue --type='json' -p='[{"op": "add", "path": "/spec/admissionChecks", "value":["check-node-capacity-before-admission"]}]'
```

---

## Functionalities

### 1. **Admission Check Controller**

- **Requeues workload admission**: Workloads which cannot be immediatelly admitted are requeued with a delay of 60 seconds

### 2. **Resource Monitoring**

- **Snapshot Logging**: Logs the current cluster resource snapshot whenever a workload is admitted.
- **Node and Pod Monitoring**: Periodically fetches and processes resource usage from nodes and pods.

---

## Testing the Controller

### 1. **Deploy a Sample Workload**

Use the provided [sample-job-limits.yaml](examples/sample-job-limits.yaml) as an example workload:

```bash
kubectl create -f sample-job-limits.yaml
```

### 2. **Monitor Controller Logs**

Check the controller logs to verify admission behavior and resource snapshot logging:

```bash
kubectl logs -n kueue-system deployment/acc-3211-controller-manager --since 3m --folow
```

Expected log entries include:
- Workload admission checks.
- Node and pod resource snapshots.

---

## Testing Gang Scheduling

To test the behavior of gang scheduling, you can use the provided script [`gang_test_runner.sh`](test/gang_test_runner.sh). This script creates multiple jobs using the Kubernetes job YAML file `sample-job-limits-gang.yaml`. The job defines a workload that requests **2 CPUs and 1Gi of memory per pod** and runs for 120 seconds. Each job creates 3 pods, which are submitted to the Kubernetes cluster for scheduling.

### How to Run

1. Ensure your cluster has sufficient resources: **more than 30 CPUs and 30Gi of memory**. If your cluster quota is lower, modify the `sample-job-limits-gang.yaml` file to reduce the `cpu` and `memory` requests accordingly.
1. Run the script:
   ```bash
   cd test
   ./gang_test_runner.sh
   ```

### What the Script Does
- Clears the `gang_test.txt` file to ensure fresh results.
- Captures the general node resource capacity and appends it to the output file.
- Creates the job (`sample-job-limits-gang.yaml`) 5 times in a loop, waiting 16 seconds between iterations.
- Logs the state of workloads at the end of the test and saves the controller logs from the last 2 minutes.

### Expected Behavior
- The initial job(s) are scheduled and run, consuming resources on the cluster.
- Subsequent job(s) may be suspended due to insufficient resources (if the cluster's available resources are less than required by the job).
- After ~60 seconds, as previous jobs complete, the controller will attempt to admit pending jobs again, leveraging the newly available resources.

```bash
./gang_test_runner.sh
Creating job (iteration 1)...
job.batch/sample-job-gang-jk4qk created
Creating job (iteration 2)...
job.batch/sample-job-gang-bzzr2 created
Creating job (iteration 3)...
job.batch/sample-job-gang-ct56t created
Creating job (iteration 4)...
job.batch/sample-job-gang-gcxft created
Creating job (iteration 5)...
job.batch/sample-job-gang-45knm created
Test completed. Results saved to gang_test.txt.

oc get workloads

---- Workloads state at the end ----

NAME                              QUEUE        RESERVED IN     ADMITTED   FINISHED   AGE
job-sample-job-gang-45knm-54d93   user-queue   cluster-queue                         16s
job-sample-job-gang-bzzr2-4898c   user-queue   cluster-queue   True                  64s
job-sample-job-gang-ct56t-6b2ef   user-queue   cluster-queue   True                  48s
job-sample-job-gang-gcxft-91f87   user-queue   cluster-queue                         32s
job-sample-job-gang-jk4qk-03e37   user-queue   cluster-queue   True                  80s

```

The test is designed to demonstrate the dynamic resource allocation and scheduling of workloads under resource constraints.




---

## Configuration Details

### Controller logic
Controller's logic is defined in [internal/controller/workload_controller.go](internal/controller/workload_controller.go) in the Reconcile function


### RBAC Permissions

- **Nodes**: Required to monitor node resources.
- **Pods**: Required to fetch pod resource details.
- **Workloads and AdmissionChecks**: Core functionality of the admission controller.

RBAC configurations are defined in:
- `config/rbac/role.yaml`
- `config/rbac/role_binding.yaml`


