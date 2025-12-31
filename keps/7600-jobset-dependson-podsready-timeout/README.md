# KEP-7600: JobSet DependsOn and WaitForPodsReady Timeout Interaction

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Story 1: Multi-Stage Pipeline with Dependency Timeout](#story-1-multi-stage-pipeline-with-dependency-timeout)
    - [Story 2: Training Pipeline with Data Preparation](#story-2-training-pipeline-with-data-preparation)
  - [Risks and Mitigations](#risks-and-mitigations)
<!-- /toc -->

## Summary

This KEP proposes changes to how the `WaitForPodsReady` timeout interacts with JobSet's `dependsOn` field.
When a JobSet is created with `dependsOn` defined and `WaitForPodsReady` is enabled, the timeout should
start counting when the workload is admitted and restart the counting when the Jobs with `dependsOn` are started,
rather than requiring the system administrator to increate the Timeout to a very high number to
accomodate the time that the dependency from JobSet `dependsOn` took to be satisfied.

## Motivation

When using JobSet with no jobs with dependsOn, all the jobs are expected to start right away and consequently
the Timeout for PodsReady=true is starting to be counted as soon as the workload is admited.
If the Timeout is reached then the workload is evicted and re-queued. However, when JobSet has jobs
with dependsOn, only the jobs without dependsOn are started right away, other jobs with DependsOn are
waiting for its dependency to be satified in order for it to start running, this means that the Timeout for the
hole workload to reach PodsReady=true would need to be increased for a value arbitrarily long to avoid having
workloads evicted when some Jobs didn't even have a chance to run as the dependency was not satisfied.

### Goals

- Ensure that JobSet containing Jobs with `dependsOn` are not evicted due to PodsReady timeout while
  not all jobs are expected to be started when workload is admitted.
- Provide fair timeout behavior that only counts time when the jobs can actually make progress.
- Maintain backward compatibility for JobSets without `dependsOn`.

### Non-Goals

- Changing the timeout behavior for JobSets without jobs with `dependsOn`
- Adding new timeout configuration options
- Change when PodsReady condition is defined

## Proposal

Modify the `WaitForPodsReady` timeout mechanism to be aware of Jobs dependency states. When a jobset 
has jobs with unsatisfied `dependsOn` dependencies:
1. Start the timeout timer when the workload is initially admitted
2. Restart the timeout when `dependsOn` dependencies are satisfied and the workload can begin creating pods, do the same for every Job that has `dependsOn` 

This ensures that the timeout only applies to the actual pod readiness waiting period, not the dependency
waiting period.

### User Stories

#### Story 1: Multi-Stage Pipeline with Dependency Timeout

As a machine learning engineer, I have a JobSet with three stages:
1. Data preprocessing job (takes 10 minutes)
2. Training job (depends on preprocessing, should start within 5 minutes of preprocessing completion and takes 6 minutes for completion)
3. Evaluation job (depends on training, should start within 5 minutes of training completion and takes 2 minutes for completion)

I configure `WaitForPodsReady.timeout=5m`.

Currently:
- Timeout starts when workload is admitted
- Data processing is started, but the workload reached timeout as not all jobs have started, workload is evicted.
- Training job didn't start
- Evaluation job didn't start
- The pipeline fails

Expected behavior with this KEP:
- Timeout starts when workloads is admitted
- Preprocessing is ready within 5 minutes and job completes successfully after 10 minutes
- When preprocessing completes, the timeout restarts
- Training job has 5 minutes to get its pods ready after preprocessing completes
- when training job completes, the timeout restarts
- Evaluation job has 5 minutes to get its pods ready after training completes
- Pipeline succeeds

### Risks and Mitigations

**Risk**: Increased complexity in timeout tracking logic
- *Mitigation*: Clear state machine design, comprehensive testing, and detailed documentation

**Risk**: Potential race conditions when dependencies transition states
- *Mitigation*: Use workload status fields to track dependency state transitions atomically

**Risk**: Backward compatibility concerns for existing workloads
- *Mitigation*: The change only affects workloads with `dependsOn` defined; existing workloads without
  dependencies continue to work as before

**Risk**: Users may not understand the new timeout behavior
- *Mitigation*: Clear documentation, status conditions indicating timeout state, and events for timeout
  resets

