# demo-acc

Implements an Demo external Admission Check Controller.

As a demonstrative behaviour, the demo-acc blocks the blocks the admission of a workload for one minute after its creation.

Check [STEP-BY-STEP](STEP-BY-STEP.md) for details on the demo-acc development.

## Test the ACC

### Install Kueue

Install kueue as described in its [installation guide](https://kueue.sigs.k8s.io/docs/installation/).

### Build the image

```bash
make docker-build
```

Make sure the test cluster has access to the built image (it can be pushed in a image repository or directly in a test cluster).

Skip `make install` since we have no API defined.

```bash
make deploy
```

Make sure the controller's manager is properly running

```bash
kubectl get -n demo-acc-system deployments.apps
```

Set up a single cluster queue environment as described [here](https://kueue.sigs.k8s.io/docs/tasks/manage/administer_cluster_quotas/#single-clusterqueue-and-single-resourceflavor-setup).
This can be done with:
```bash
kubectl apply -f https://kueue.sigs.k8s.io/examples/admin/single-clusterqueue-setup.yaml
```

Create an AdmissionCheck managed by demo-acc

```bash
kubectl apply -f - <<EOF
apiVersion: kueue.x-k8s.io/v1beta1
kind: AdmissionCheck
metadata:
  name: demo-ac
spec:
  controllerName: experimental.kueue.x-k8s.io/demo-acc
EOF
```

It should get marked as `Active`

```bash
kubectl get admissionchecks.kueue.x-k8s.io demo-ac -o=jsonpath='{.status.conditions[?(@.type=="Active")].status}{" -> "}{.status.conditions[?(@.type=="Active")].message}{"\n"}'
```

Should output: `True -> demo-acc is running`

Add the admission check to the `cluster-queue` queue:

```bash
kubectl patch clusterqueues.kueue.x-k8s.io cluster-queue --type='json' -p='[{"op": "add", "path": "/spec/admissionChecks", "value":["demo-ac"]}]'
```

Create a sample-job:

```bash
kubectl create -f https://kueue.sigs.k8s.io/examples/jobs/sample-job.yaml
```

Observe the workload's execution.

Given the job is created at t0
- Since the queue is not used, it will get its quota reserved at t0
- demo-acc will marks its admission check state at t0+1m
- it will be admitted at t0+1m

For example:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Workload
metadata:
  creationTimestamp: "2024-10-18T12:37:15Z" #t0
  #<skip>
spec:
  active: true
  #<skip>
status:
  admission:
    clusterQueue: cluster-queue
    #<skip>
  admissionChecks:
  - lastTransitionTime: "2024-10-18T12:38:15Z" #t0+1m
    message: The workload is now ready
    name: demo-ac
    state: Ready
  conditions:
  - lastTransitionTime: "2024-10-18T12:37:15Z" #t0
    message: Quota reserved in ClusterQueue cluster-queue
    observedGeneration: 1
    reason: QuotaReserved
    status: "True"
    type: QuotaReserved
  - lastTransitionTime: "2024-10-18T12:38:15Z" #t0+1m
    message: The workload is admitted
    observedGeneration: 1
    reason: Admitted
    status: "True"
    type: Admitted
  - #<skip>
```
