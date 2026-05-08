---
name: kueue-flake-debugger
description: Expertise in debugging Kueue flakes
---

# Kueue flake debugger

You are an expert in Kueue which is the project for Workload orchestration.

## Expertise

- Deep understanding of Kueue.

## Flake debugging

### Step one - initial build-log analysis

When asked to debug a flake with the given link to Github issue then identify the prow link to the failure.

Please report the list of all prow links. Try using `gh` first, if available,
otherwise fallback to other tools you have, for example curl:

```sh
# Note: If the issue context is already provided or visible, you can skip this automated curl and manually identify the Prow link from the issue description.
curl -Lv https://github.com/kubernetes-sigs/kueue/issues/ABCD 2>/dev/null | grep -e "href=\"https://prow\.k8s\.io/view/gs/kubernetes-ci-logs/pr-logs/pull"
```

Then, extract the links.

Choose the first, let call it PROW_LOG, eg:

```sh
BASE_PROW_LOG=https://gcsweb.k8s.io/gcs/kubernetes-ci-logs/pr-logs/pull/kubernetes-sigs_kueue/9617/pull-kueue-test-e2e-main-1-33/2028383367677874176
```

Then appenend "/build-log.txt", down load using curl, say

```sh
curl -Lv ${BASE_PROW_LOG}/build-log.txt -obuild-logs/build-log.txt
```
Note, always put all artifacts under the `build-logs/` folder.

Then grep the build-log around the failing lines, for example:

```sh
cat build-logs/build-log.txt | grep -ab40 "FAILED"
```
Output the lines, summarize the failure line, the name of the failed test and the namespace name.

### Step 2 - identify the artifacts location with worker and control-plane Pods

To do that fetch the URL: "${BASE_PROW_LOG}/artifacts/", you will find a URL with the suffix like `FAILED_SUITE=run-test-e2e-singlecluster-1.33.7`
then identify the BASE_ARTIFACTS directory, for example:

```sh
BASE_ARTIFACTS=${BASE_PROW_LOG}/${FAILED_SUITE}
```
For example: `BASE_ARTIFACTS=https://gcsweb.k8s.io/gcs/kubernetes-ci-logs/pr-logs/pull/kubernetes-sigs_kueue/9617/pull-kueue-test-e2e-main-1-33/2028383367677874176/artifacts/run-test-e2e-singlecluster-1.33.7`

### Step 3 - identify names of control-plane Pods

The next important steps of debugging is to identify location of the relevant Pods.

This requires checking what are the control-plane Pods, but fetching: "${BASE_ARTIFACTS}/kind-control-plane/pods/"

*Tip: When curling GCS bucket directories, ensure you include the trailing slash to receive an HTML directory listing, which you can then parse using `grep href` to find the exact subdirectory.*

There you will find names for the control-plane logs, for example for Kube-scheduler you may find `KUBE_SCHEDULER_POD=kube-system_scheduler-kind-control-plane_3d2cfc9019893b83637280170b3fd08f`

### Step 4 - analyze kube-scheduler logs

Analyze the kube-scheduler logs which you find under the link.

First fetch the kube-scheduler log names from:

```sh
KUBE_SCHEDULER_LOGS=${BASE_ARTIFACTS}/kind-control-plane/pods/${KUBE_SCHEDULER_POD}/kube-scheduler/
```
You will find there the list of files, for exampl 0.log. So the final kube-scheduler log might look like: `${KUBE_SCHEDULER_LOGS}/0.log`.

Now, identify the placement of relevant Pods:

1. find placement of the kueue-controller-manager Pods around the test failure.

2. find the placement of Pods by the namespace corresponding to the failed test.

You are looking for text anchors like "Successfully bound pod to node", for example: "2026-03-02T08:28:59.236232048Z stderr F I0302 08:28:59.235983       1 schedule_one.go:314] "Successfully bound pod to node" pod="kueue-system/kueue-controller-manager-787d6db649-sfhqv" node="kind-worker2" evaluatedNodes=3 feasibleNodes=2" means the Kueue pod was placed on kind-worker2.

### Step 5 - analyze kubelet logs

Once you know the placement of all relevant Pods, please to the the kubelet Pods
for each of them. For example, for the link for the failed suite for kubelet
on the (eg `KIND_WORKER=kind-worker2`) might be found under teh link:

`${BASE_ARTIFACTS}/${KIND_WORKER}/kubelet.log`

It is useful to check Kubelet logs from all workers, so often also `kind-worker2` etc.

Summarize what you can find relevant in the kubelet logs. *Tip: Kubelet logs are notoriously noisy. To find relevant signals, `grep` explicitly for the namespace, pod name, or pod UID discovered in the previous steps.*

### Step 6 - match the failed test to the test code

Now, read the test code around the failure. for example, if the failed test indicates
`/home/prow/go/src/sigs.k8s.io/kueue/test/e2e/singlecluster/job_test.go:535` then read the `test/e2e/singlecluster/job_test.go`
around the failed line. Read at least 100 lines before the failed line to understand the BeforeEach, BeforeAll etc.

Summarize your findings.

### Step 7 - reason about the failure

Now, think about the possible reason for the failure. Combine the findings from
all the previous steps.

### Step 8 - recommendations and final summary

Suggest recommendations to fix.

In the final summary when you are making a statement, you must back that statement up with evidence and citations. The citations could be a log file, a source code file, documentation etc. When citing file you should include it in human readable way, to make verification of your claims quick and easy, for e.g. filepath:linenumber. For every point, when evidence available, provide a link (clickable when rendered in markdown) to the log file supporting the claim.