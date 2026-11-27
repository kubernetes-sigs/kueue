# Integration coverage collection (issue #18429)

This change adds a separate integration coverage profile to the existing
single-cluster and MultiKueue integration shards. It is the collection stage
of #18429, not the cross-shard merging or Codecov publishing stage.

## Collection and artifacts

- Existing Prow jobs run make test-integration (four shards) and
  make test-multikueue-integration (one job); their commands do not change.
- Ginkgo uses --race, --cover, --coverpkg=sigs.k8s.io/kueue/... and
  --coverprofile=integration-cover.out. Ginkgo combines suites and writes
  one atomic-mode profile under --output-dir=$(ARTIFACTS).
  Coverprofile must be a filename, not a path.
- Unit and integration share UNIT_COVERAGE_EXCLUDE and the same filtering
  helper. The helper sums counts in atomic/count modes and applies Boolean
  union in set mode. It fails without overwriting a previous good profile.
- Each integration shard writes integration-cover.out (raw),
  filtered.cov (per-job Spyglass coverage lens), and filtered.html,
  alongside its existing Ginkgo JSON report.

Profiles are cleared before each run to avoid publishing stale data.
Use INTEGRATION_COVERAGE=0 make test-integration (or the MultiKueue target)
to benchmark the uninstrumented run or troubleshoot coverage overhead.
Coverage is enabled by default for presubmit collection.

## Tests

Run make verify-filter-coverage verify-integration-coverage.
The tests exercise filtering, duplicate blocks, invalid input and the actual
Make targets with fake Ginkgo and Go commands. No cluster is required.
Prow still needs to measure the real elapsed time, RAM and profile size.

## Follow-up after PR #18135

#18135 is still an open dependency introducing a unit-shard Prow merging
script and jobs. Extend that logic instead of duplicating its presubmit-ref
matching and postsubmit tested-tree matching:

1. Collect the four integration shards and the MultiKueue integration job on
   exactly matching refs (or tested trees). Check recorded shard counts.
2. Produce integration.cov and a distinct TestGrid JUnit coverage summary.
   Keep the existing unit percentage separate. A combined profile may be
   added if its count semantics are carefully defined.
3. Decide whether the one-file Spyglass lens displays unit, integration or
   combined coverage before modifying the coverage job's filtered.cov.
4. After #18136, upload integration to Codecov under a separate flag.
5. Benchmark Prow shard duration, RAM and artifacts before making the
   coverage job wait for all integration shards or renaming the jobs.

No public Kueue API changes are involved.
