# KEP-1466: SLSA build provenance for Kueue release artifacts

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [Terminology](#terminology)
  - [User Stories](#user-stories)
    - [Story 1: A cluster admin cannot tell which commit built a released Kueue image](#story-1-a-cluster-admin-cannot-tell-which-commit-built-a-released-kueue-image)
    - [Story 2: A platform team wants to reject Kueue images without build provenance at admission time](#story-2-a-platform-team-wants-to-reject-kueue-images-without-build-provenance-at-admission-time)
    - [Story 3: An admin cannot tell which commit built the Kueue Helm chart they install](#story-3-an-admin-cannot-tell-which-commit-built-the-kueue-helm-chart-they-install)
  - [Notes/Constraints/Caveats](#notesconstraintscaveats)
  - [Risks and Mitigations](#risks-and-mitigations)
    - [A Sigstore outage can delay staging builds and releases](#a-sigstore-outage-can-delay-staging-builds-and-releases)
    - [Anyone who can act as the build identity can sign false provenance](#anyone-who-can-act-as-the-build-identity-can-sign-false-provenance)
    - [An unattested artifact can be promoted during the transition period](#an-unattested-artifact-can-be-promoted-during-the-transition-period)
    - [Consumers may assume L1 provenance gives stronger guarantees than it does](#consumers-may-assume-l1-provenance-gives-stronger-guarantees-than-it-does)
- [Design Details](#design-details)
  - [Attested artifacts](#attested-artifacts)
  - [Build identity](#build-identity)
  - [Build identity rollout](#build-identity-rollout)
  - [Attestation step](#attestation-step)
  - [Provenance content](#provenance-content)
  - [Failure handling](#failure-handling)
    - [Release-time attestation checks](#release-time-attestation-checks)
    - [Verification summaries](#verification-summaries)
    - [Disabling build provenance](#disabling-build-provenance)
  - [Promotion](#promotion)
    - [How the promoter carries attestations](#how-the-promoter-carries-attestations)
    - [Staging retention](#staging-retention)
    - [Releases promoted before the policy](#releases-promoted-before-the-policy)
    - [Moving the policy to require](#moving-the-policy-to-require)
  - [Release candidates](#release-candidates)
  - [Release branches](#release-branches)
  - [Changes in this repository](#changes-in-this-repository)
  - [Changes in other repositories](#changes-in-other-repositories)
  - [Interactions](#interactions)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
      - [Policy dry-run tests](#policy-dry-run-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha](#alpha)
    - [Beta](#beta)
    - [Stable](#stable)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Future work ideas](#future-work-ideas)
- [Appendix: External terms](#appendix-external-terms)
- [Appendix: Tool version requirements](#appendix-tool-version-requirements)
<!-- /toc -->

## Summary

This KEP adds signed SLSA v1 build provenance to Kueue's staging images and Helm charts.
Consumers can then verify which source commit and build produced the artifacts promoted to the production registry.

## Motivation

Staging artifacts carry no build provenance today.
Promoted images carry a promotion signature, but no record of the build that produced them.
A promotion signature answers "was this artifact promoted through the official pipeline?".
It does not answer "which commit built it?".
The image promoter can carry build provenance to the production registry, but only when the staging artifact already carries it.
Release consumers (cluster admins, platform teams, and security auditors) cannot verify the build origin of Kueue images.

The Security Profiles Operator (SPO) piloted this pattern in the Kubernetes ecosystem; SPO v1.1.1 was the first release promoted with build provenance.

### Goals

- Every promoted image and Helm chart carries a Sigstore-signed SLSA v1 build provenance attestation on staging.
- The attestation survives promotion to the production registry.
- Release candidate images and charts are attested on staging by the same build.
- Consumers can verify provenance with a documented signing identity and issuer.
- A failed attestation step fails the build visibly.

### Non-Goals

- Provenance for GitHub release assets (`kubectl-kueue` binaries, rendered manifests, raw Helm chart tarballs). These are future work.
- SLSA Build Level 2 or higher.
- Changes to Kueue's Kubernetes APIs, configuration, or controller behavior.
- Attesting periodic build images (debug, importer, ray-mini).
- Promoting release candidates to the production registry; see [Alternatives](#alternatives).
- Retroactive attestation of past releases.

## Proposal

The staging build signs build provenance for every release image and Helm chart.
The provenance stays with each artifact when the artifact is promoted to the production registry.
Consumers check the provenance against a documented Kueue build identity before they deploy Kueue.

### Terminology

- **Staging artifact**: an image or Helm chart published to the staging Artifact Registry by a postsubmit build.
- **Promoted artifact**: a staging artifact copied to the production registry by the image promoter (`kpromo`, from kubernetes-sigs/promo-tools).
- **Build provenance**: a signed statement of which source, build system, and invocation produced an artifact.
- **Promotion signature**: a signature added by the image promoter at copy time. It attests the copy, not the build.
- **Kueue promoter policy**: the provenance policy for Kueue in the kpromo promoter manifest in kubernetes/k8s.io, in `warn` or `require` mode.
  It is distinct from the Sigstore Policy Controller `ClusterImagePolicy` in Story 2.
- **Build identity** (`kueue-sa`): the service account that runs the staging build. Its email address is the Sigstore signing identity.

For SLSA, Sigstore, cosign, Fulcio, Rekor and OCI referrer, see [Appendix: External terms](#appendix-external-terms).

### User Stories

#### Story 1: A cluster admin cannot tell which commit built a released Kueue image

An admin wants to verify the build provenance of a released Kueue image and confirm which commit built it.
They verify the attestation of the image digest and read the source commit from the predicate.
Use the multi-arch image index digest, not a platform manifest digest.
This story applies to released (tagged) versions, where the predicate records the tag ref.
For images built from `main` or `release-*` branches, the predicate records the branch ref; use the commit SHA from `externalParameters.source.digest.gitCommit` to identify the exact build.
Resolve the release tag to its multi-arch index digest with `crane` (part of `go-containerregistry`):

```bash
crane digest registry.k8s.io/kueue/kueue:v<version>
# sha256:<multi-arch-index-digest>
```

Use the output digest as `<multi-arch-index-digest>` in the commands below.
Release candidates and branch builds are not promoted; to verify one, replace `registry.k8s.io/kueue/kueue` with `us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue` in the `crane` and `cosign` commands below.
The `cosign` commands need cosign v3.0.1 or later, or v2.5.0 to v2.6.x with `--new-bundle-format`; see [Appendix: Tool version requirements](#appendix-tool-version-requirements).

```bash
cosign verify-attestation \
  --type slsaprovenance1 \
  --certificate-identity kueue-sa@k8s-staging-images.iam.gserviceaccount.com \
  --certificate-oidc-issuer https://accounts.google.com \
  registry.k8s.io/kueue/kueue@sha256:<multi-arch-index-digest>
```

The predicate records the source and the commit:

```json
{
  "buildDefinition": {
    "externalParameters": {
      "source": {
        "uri": "git+https://github.com/kubernetes-sigs/kueue@refs/tags/v<version>",
        "digest": { "gitCommit": "<commit-sha>" }
      }
    }
  },
  "runDetails": {
    "builder": { "id": "https://cloudbuild.googleapis.com/projects/k8s-staging-images/serviceAccounts/kueue-sa@k8s-staging-images.iam.gserviceaccount.com/cloudbuild.yaml" }
  }
}
```

This excerpt shows only the source commit and builder fields relevant to commit verification.
After `cosign verify-attestation` succeeds, compare `externalParameters.source.uri` with the expected tag (`git+https://github.com/kubernetes-sigs/kueue@refs/tags/v<version>`) and `externalParameters.source.digest.gitCommit` with the known commit SHA for that release.
A mismatch means the attestation was signed for a different build; reject it and do not treat the image as verified.
The full predicate shape, including `buildType`, `resolvedDependencies`, and invocation ID, is described in [Provenance content](#provenance-content).

Consumer documentation covers extracting and inspecting the predicate.

If the image has no attestation, `cosign` exits with a non-zero status and prints a message such as `no matching attestations`.
This is expected for an image from an interrupted build; for releases without an attestation, see [Promotion](#promotion).

Staging attestations of release candidates and branch builds stay verifiable only for a limited time; see [Promotion](#promotion).

#### Story 2: A platform team wants to reject Kueue images without build provenance at admission time

A platform team runs the [Sigstore Policy Controller](https://docs.sigstore.dev/policy-controller/overview/) in their clusters.
The `ClusterImagePolicy` below is a Policy Controller resource and has no effect without it.
It needs Policy Controller v0.13.0 or later, the first release that supports `signatureFormat: bundle`; see [Appendix: Tool version requirements](#appendix-tool-version-requirements).
The Policy Controller admits Kueue images only when a provenance attestation from the Kueue build identity is present.
When attestation verification fails — no matching provenance, wrong identity, or invalid signature — Policy Controller denies admission and returns a policy violation to the caller.
The policy below rejects every Kueue release without an attestation; see [Promotion](#promotion) for which releases have none:

```yaml
apiVersion: policy.sigstore.dev/v1beta1
kind: ClusterImagePolicy
metadata:
  name: kueue-provenance
spec:
  images:
    - glob: "registry.k8s.io/kueue/**"
  authorities:
    - keyless:
        identities:
          - issuer: https://accounts.google.com
            subject: kueue-sa@k8s-staging-images.iam.gserviceaccount.com
      signatureFormat: bundle
      attestations:
        - name: build-provenance
          predicateType: https://slsa.dev/provenance/v1
```

Consumer documentation also covers admission policies that check kpromo verification summaries instead of the Kueue build identity.

#### Story 3: An admin cannot tell which commit built the Kueue Helm chart they install

An admin installs Kueue from the Helm chart at `oci://registry.k8s.io/kueue/charts/kueue`.
Before installing, they want to confirm which commit built the chart.
Installing a chart from an OCI registry needs Helm 3.8.0 or later, and verification needs the cosign version in Story 1; see [Appendix: Tool version requirements](#appendix-tool-version-requirements).
The predicate has the same shape as in Story 1, with the chart digest as the attestation subject.

After `cosign verify-attestation` succeeds, compare `externalParameters.source.uri` with the expected release tag and `externalParameters.source.digest.gitCommit` with its commit SHA, as in [Story 1](#story-1-a-cluster-admin-cannot-tell-which-commit-built-a-released-kueue-image).
If `cosign` exits with a non-zero status, or either field does not match, do not install the chart.
For charts of releases without an attestation, see [Promotion](#promotion).

First resolve the digest, then verify the attestation:

```bash
crane digest registry.k8s.io/kueue/charts/kueue:<chart-version>
# sha256:<digest>

cosign verify-attestation \
  --type slsaprovenance1 \
  --certificate-identity kueue-sa@k8s-staging-images.iam.gserviceaccount.com \
  --certificate-oidc-issuer https://accounts.google.com \
  registry.k8s.io/kueue/charts/kueue@sha256:<digest>
```

Install by digest, so the installed chart is the one verified above:

```bash
helm install kueue oci://registry.k8s.io/kueue/charts/kueue@sha256:<digest>
```

For a release candidate chart, which is not promoted, use `us-central1-docker.pkg.dev/k8s-staging-images/kueue/charts/kueue` instead of `registry.k8s.io/kueue/charts/kueue` in these commands.

A chart attestation does not cover the images that the chart deploys; verify those separately, as in Story 1.

### Notes/Constraints/Caveats

- The level achieved is SLSA Build L1.
  The build writes its own provenance predicate, and build steps are not isolated from each other.

### Risks and Mitigations

#### A Sigstore outage can delay staging builds and releases

A staging build cannot sign or verify while Sigstore is unavailable.
The Sigstore public-good instance (Fulcio and Rekor) has a published 99.5% availability SLO and an uptime history at [status.sigstore.dev](https://status.sigstore.dev), but no contractual SLA.
The attestation build step runs after the image and chart pushes, so an outage does not block those pushes; it fails the build at the attestation or self-check step.
Rerun the postsubmit after the outage, or, for an extended outage, build with [`SIGN=false`](#attestation-step) and skip the attestation wait and the ref and commit check in `/wait-for-images`, and the attestation verification and the ref and commit check in the release promotion script.
Once the Kueue promoter policy is in `require` mode, `pull-k8sio-cip` fails a promotion pull request that adds an unattested digest, so an outage delays a release but cannot promote an unattested artifact.
A release built with `SIGN=false` can then be promoted only after the policy moves back to `warn` or is removed; see [Disabling build provenance](#disabling-build-provenance).

#### Anyone who can act as the build identity can sign false provenance

After step 3 of [Build identity rollout](#build-identity-rollout), access to the service account is limited to staging project administrators and the Cloud Build pipeline.
Consumers must pin both the signing identity and the issuer when they verify.
Consumers must not treat L1 provenance as tamper-proof.

#### An unattested artifact can be promoted during the transition period

The Kueue promoter policy starts in `warn` mode, so an artifact without valid provenance can still be promoted until the policy moves to `require`.
This transition period ends after the first Kueue promotion that passes the policy; see [Promotion](#promotion).
A failed attestation fails the build visibly through the self-check, but a build with `SIGN=false` still succeeds, which bypasses that goal.
Only two paths can set `SIGN=false`: a reviewed change to the staging build definition in this repository, or a manual build submitted by staging project administrators; the access limits in [Anyone who can act as the build identity can sign false provenance](#anyone-who-can-act-as-the-build-identity-can-sign-false-provenance) apply to the second path.
During the transition period, the release manager's `pull-k8sio-cip` log check in [Promotion](#promotion) keeps these digests from merging.
Under `require`, the promoter rejects them.
Removing the policy has the same effect, and `pull-k8sio-cip` then logs no violation; see [Disabling build provenance](#disabling-build-provenance).

#### Consumers may assume L1 provenance gives stronger guarantees than it does

The consumer documentation, tracked separately, states the SLSA level and its limits.

## Design Details

### Attested artifacts

| Artifact | Type | Phase |
|---|---|---|
| kueue | Image | 1 (this KEP) |
| kueueviz-backend | Image | 1 (this KEP) |
| kueueviz-frontend | Image | 1 (this KEP) |
| kueue-populator | Image | 1 (this KEP) |
| kueue-priority-booster | Image | 1 (this KEP) |
| kueue chart | Helm chart | 1 (this KEP) |
| kueue-populator chart | Helm chart | 1 (this KEP) |
| kueue-priority-booster chart | Helm chart | 1 (this KEP) |
| Periodic build images (debug, importer, ray-mini) | Image | Not attested |
| GitHub release assets | Binary and manifest | Future work |

Attestations attach to the multi-arch index digest that the image promoter references.
Platform-specific manifests do not carry their own attestation.
The image promoter promotes the index digest, and `cosign verify-attestation` resolves attestations from the index.
This differs from projects such as Kubernetes releases, whose build attests both the index and every per-arch manifest.

### Build identity

The staging build runs as a dedicated service account, `kueue-sa`, in the staging project.
The account already has Artifact Registry write access and OIDC token-creator permissions.
The build obtains a Fulcio identity certificate from its ambient Google identity, so it needs no extra credentials.
Consumers verify against the account's email address and the Google OIDC issuer.

### Build identity rollout

Three changes move the staging build from `gcb-builder` to `kueue-sa`.
They land in this order.
A staging build of `main` must succeed after each step before the next step starts.

| Step | Repository | Change | Why the staging build keeps working |
|---|---|---|---|
| 1 | kubernetes-sigs/kueue | The staging build definition sets `serviceAccount: projects/k8s-staging-images/serviceAccounts/kueue-sa@k8s-staging-images.iam.gserviceaccount.com`, on `main` and every supported release branch | The job still runs as `gcb-builder`, which can still act as `kueue-sa` through the `gcb-builder` migration shim |
| 2 | kubernetes/test-infra | The staging build job sets `serviceAccountName: kueue` instead of `gcb-builder` | Every supported branch already requests `kueue-sa`, and the `kueue` identity can act as `kueue-sa` |
| 3 | kubernetes/k8s.io | Add `kueue` to `skip_gcb_builder_shim` | No build that requests `kueue-sa` runs as `gcb-builder` anymore, so removing its access to `kueue-sa` breaks nothing |

Landing step 3 before step 2, or step 2 before a branch carries step 1, stops the staging build of that branch.
For example, if step 3 lands while the job still runs as `gcb-builder`, Cloud Build rejects every build that requests `kueue-sa`, and nothing is pushed.
The same job builds `release-*` branches and tags, so release branches that carry step 1 break too.

The [attestation step](#attestation-step) signs as `kueue-sa`, so on `main` and on each release branch it merges together with or after step 1.

The periodic build of unattested images keeps running as `gcb-builder` and does not request `kueue-sa`, so step 3 does not affect it.

Until step 3 lands, any `gcb-builder` postsubmit can still submit a build as `kueue-sa` and sign "Kueue provenance".
The Kueue promoter policy in `warn` mode therefore merges only after step 3; see [Release branches](#release-branches).

### Attestation step

1. The build records the digest of each multi-arch image index and each chart from its own push, and never resolves it from a tag.
   A tag can move between the push and a later lookup, for example when a concurrent build pushes the same tag, so a tag lookup could attest a different artifact.
   Images built in parallel record their digests separately, so their digests do not mix.
   The tag is used only as the push target.
2. The build writes a SLSA v1 predicate that records the source repository URI, the git ref
   that triggered the build, the git commit SHA, the builder identity, and the Cloud Build
   invocation ID. For tag builds, the ref identifies the commit. For `main` and `release-*`
   branch builds, the ref is the branch name and `externalParameters.source.digest.gitCommit`
   identifies the build.
   The ref is the branch or tag that triggered the build, which the Prow image-builder passes as the `_PULL_BASE_REF` Cloud Build substitution: a short name such as `main`, `release-0.15`, `v0.15.0`, or `v0.15.0-rc.1`.
   A name that matches the semver tag pattern of the staging postsubmit job (`vX.Y.Z`, optionally with a pre-release suffix such as `-rc.N`) becomes `refs/tags/<name>`; any other name becomes `refs/heads/<name>`.
   The commit is the HEAD commit of the checked-out source.
   `externalParameters.source.uri` is then `git+https://github.com/kubernetes-sigs/kueue@<full-ref>`.
3. The build attaches the predicate as a Sigstore bundle OCI referrer to the digest recorded in Step 1, signed with the build service account's workload identity.
   Steps 2 to 4 run after all image and chart pushes complete, in a build step whose image is pinned by digest.
   The image digest is set in the implementation PR.
   Each push writes its recorded digest to its own file in `/workspace`, which Cloud Build shares between build steps.
   The attestation step reads digests only from those files.
4. The build verifies the attestation on the same digest against the build identity as a
   self-check, and fails if verification fails.

The exact commands are defined in the implementation PR and documented in the release runbook.

`SIGN=false` is a staging build setting that skips signing, attestation and the self-check; the build still succeeds and pushes images and charts without attestations.
These steps run by default.
`SIGN=false` is the supported way to disable the feature, for example during an extended Sigstore outage; see [Disabling build provenance](#disabling-build-provenance).
A build with `SIGN=false` also needs the options that skip the attestation wait and the ref and commit check in `/wait-for-images`, and the attestation verification and the ref and commit check in the release promotion script.
A local dry-run mode writes the predicate without signing.

### Provenance content

The predicate has this shape. Values in angle brackets vary per build.
Per SLSA v1, `runDetails.builder.id` identifies the entity that produces and signs the predicate.
The build writes and signs its own provenance, so the builder is the repository's Cloud Build configuration running as the `kueue-sa` service account — not the Prow job that submitted the build.
SPO and the promo-tools provenance policy documentation use this form:
`https://cloudbuild.googleapis.com/projects/<project>/serviceAccounts/<sa>/cloudbuild.yaml`
For Kueue: `https://cloudbuild.googleapis.com/projects/k8s-staging-images/serviceAccounts/kueue-sa@k8s-staging-images.iam.gserviceaccount.com/cloudbuild.yaml`
This value is also what the Kueue promoter policy's `builders[].id` field must match (see [Changes in other repositories](#changes-in-other-repositories)).

**Note:** The `buildType` URI is pinned to the commit recorded in `gitCommit`, so it resolves to the release runbook at that exact revision and cannot change after the attestation is signed.
Because the URI changes with every commit, policies cannot match `buildType` against a fixed value; neither the Kueue promoter policy nor the Story 2 `ClusterImagePolicy` checks it.
A later version of the build type can use a stable `buildType` URI for new attestations if consumers need to match on it.

```json
{
  "buildDefinition": {
    "buildType": "https://github.com/kubernetes-sigs/kueue/blob/<commit>/RELEASE.md#staging-artifact-provenance-v1",
    "externalParameters": {
      "source": {
        "uri": "git+https://github.com/kubernetes-sigs/kueue@refs/tags/v<version>",
        "digest": { "gitCommit": "<commit-sha>" }
      }
    },
    "internalParameters": {
      "buildConfigSource": "cloudbuild.yaml"
    },
    "resolvedDependencies": [
      { "uri": "git+https://github.com/kubernetes-sigs/kueue", "digest": { "gitCommit": "<commit-sha>" } }
    ]
  },
  "runDetails": {
    "builder": { "id": "https://cloudbuild.googleapis.com/projects/k8s-staging-images/serviceAccounts/kueue-sa@k8s-staging-images.iam.gserviceaccount.com/cloudbuild.yaml" },
    "metadata": { "invocationId": "https://console.cloud.google.com/cloud-build/builds/<build-id>?project=<project-number>" }
  }
}
```

For a `main` or `release-*` branch build, only `externalParameters.source.uri` differs, following the ref rule in [Attestation step](#attestation-step):

```json
"source": {
  "uri": "git+https://github.com/kubernetes-sigs/kueue@refs/heads/main",
  "digest": { "gitCommit": "<commit-sha>" }
}
```

`externalParameters.source` records the user-supplied input (the git ref); `resolvedDependencies` records the resolved, pinned dependency (the specific commit). Both reference the same commit SHA for tag and branch builds.

### Failure handling

A failed attestation or self-check step fails the build with a non-zero exit code.
If Step 3 (attachment) fails, the artifact has no attestation in the staging registry.
If Step 4 (self-check) fails, the attestation is present but unverified.
In either case, rerunning the postsubmit signs the artifact again.
The earlier push stays in the registry.
The release promotion script resolves each release tag to a digest once, and records that digest in the promoter manifest pull request.
`kpromo` promotes exactly the recorded digests; it does not resolve tags again.
If the tag still points to the unattested push when the script runs, the script's attestation verification rejects that digest; see [Release-time attestation checks](#release-time-attestation-checks).
Under `require`, `pull-k8sio-cip` also fails a pull request that records an unattested digest, and the promoter does not promote it.
The release manager runs the release promotion script again after the rerun postsubmit passes its self-check.
For the period before the policy moves to `require`, see [Promotion](#promotion).

#### Release-time attestation checks

`/wait-for-images` runs before the release promotion script and usually catches an unattested push.
For each staging image and chart digest of the release, it waits until `cosign verify-attestation` against the build identity passes.
It then checks each verified attestation of that digest against the release tag.
`externalParameters.source.uri` must be `git+https://github.com/kubernetes-sigs/kueue@refs/tags/v<version>`, where `v<version>` is the release tag.
`externalParameters.source.digest.gitCommit` must be the commit that the release tag points to, with an annotated tag resolved to its commit.
On a mismatch, `/wait-for-images` fails without waiting for its timeout, and the release manager investigates the build before running the release promotion script.
A tag can move after `/wait-for-images` passes, for example when a rerun postsubmit or a manual build pushes it again.
The release promotion script therefore runs the same ref and commit check on each digest that it records in the promoter manifest.
It first verifies the attestation of that digest against the build identity, once and without waiting.
If any digest has no verified attestation or fails the check, the script exits without opening the promotion pull request.
The Kueue promoter policy checks only the signer, the builder and the source repository, so the checks in `/wait-for-images` and the release promotion script are the only release-time checks of the ref and commit.
The option that skips the attestation wait also skips the ref and commit check, because a release built with `SIGN=false` has no attestation.
`/wait-for-images` is a release ChatOps command that accepts no arguments today, so the release ChatOps parser gains a way to request this option.
The release promotion script has its own option that skips its attestation verification and its ref and commit check, for the same reason.
`/wait-for-prod-images` only waits for the promoted images and charts to appear on `registry.k8s.io`.
It runs neither the attestation wait nor the ref and commit check, because those checks run before promotion on staging and releases promoted before the Kueue promoter policy have no attestation on `registry.k8s.io`.

#### Verification summaries

A digest's verification summary records the result of its first promotion — a `FAILED` summary is permanent for that digest.
Recent Kueue releases on `registry.k8s.io`, such as `v0.20.4`, already carry [verification summaries](https://github.com/kubernetes-sigs/promo-tools/blob/main/docs/image-promotion.md#verification-summaries) with `SLSA_BUILD_LEVEL_UNEVALUATED`.
A `FAILED` summary from an unattested promotion would be visible alongside them.
[Release branches](#release-branches) orders the steps so that releases with staging attestations get no `FAILED` summary.
For the verification summaries of releases promoted without the policy, see [Promotion](#promotion).

#### Disabling build provenance

To disable build provenance, build with [`SIGN=false`](#attestation-step) or revert the attestation step.
Then run `/wait-for-images` with the option that skips the attestation wait and the ref and commit check; without it, the step fails after its one-hour timeout on the unattested digests.
Run the release promotion script with its option that skips its attestation verification and its ref and commit check; without it, the script exits without opening the promotion pull request.
If the Kueue promoter policy is in `require` mode, first move it back to `warn` in kubernetes/k8s.io; otherwise the promoter rejects every unattested digest.
Under `warn`, each unattested digest promoted gets a permanent `FAILED` summary.
To promote unattested digests without `FAILED` summaries, delete the Kueue promoter policy block from the promoter manifest instead; those digests then get `SLSA_BUILD_LEVEL_UNEVALUATED` summaries, as before the policy existed.
Removing the policy also stops kpromo from carrying attestations for every digest promoted while it is absent.
The resulting risk is described in [An unattested artifact can be promoted during the transition period](#an-unattested-artifact-can-be-promoted-during-the-transition-period).

### Promotion

Promotion requires kpromo v4.7.0 or later, which adds per-project provenance policies and OCI referrer carry.

#### How the promoter carries attestations

kpromo verifies the build attestation when present and carries it to the production registry as an OCI referrer, for Helm charts as well as container images.
kpromo evaluates the Kueue promoter policy against the index digest recorded in the promoter manifest.
An index with valid provenance about its own digest satisfies the policy on that alone, so per-arch provenance is not required.
This behavior was verified against kpromo v4.8.0 and its [provenance policy documentation](https://github.com/kubernetes-sigs/promo-tools/blob/main/docs/image-promotion.md#provenance-policies).
Per-arch manifests that are not individually attested receive `SLSA_BUILD_LEVEL_UNEVALUATED` verification summaries from kpromo.
Carrying the attestation also requires the Kueue promoter policy (see [Changes in other repositories](#changes-in-other-repositories)).
kpromo carries nothing for a digest promoted without that policy.
The promotion signature is added after the copy and is unaffected.

#### Staging retention

The staging project's Artifact Registry cleanup policy deletes staging artifacts and their attestations after 90 days.
Attestations of artifacts that are not promoted, such as release candidates and branch builds, stay verifiable in staging only until that cleanup deletes them.

#### Releases promoted before the policy

After the policy is added, the production promotion jobs carry the attestations of already promoted digests only for digests added in the last 14 days.
Kueue releases promoted before the policy therefore have no attestation on `registry.k8s.io`.
The exceptions are releases with staging attestations promoted at most 14 days before the policy merges, or covered by a promoter run without that limit while their staging images still exist.
All releases promoted before the policy keep the verification summary of their first promotion: `SLSA_BUILD_LEVEL_UNEVALUATED`, or none for digests promoted before kpromo wrote summaries, such as `v0.20.0`.
Once the Kueue promoter policy exists, a promoter run without the 14-day limit writes a `FAILED` summary for each digest without a summary whose staging image still exists, and nothing for the others, whose staging images are already deleted.
As in the Security Profiles Operator, these digests are neither exempted nor backfilled, and the consumer documentation lists the releases without a passed summary.
[Release branches](#release-branches) orders the steps so that the policy exists before the first promotion of a release with staging attestations.

#### Moving the policy to require

The Kueue promoter policy starts in `warn` mode.
It moves to `require` after the first Kueue promotion under `warn` in which kpromo records a `PASSED` verification summary with the verified level `SLSA_BUILD_LEVEL_1` for every index digest that the promotion pull request adds to the promoter manifest.
The release manager checks these summaries as described in [e2e test 4](#e2e-tests).
`PASSED` alone does not show that the policy applied: digests promoted without a policy, such as `v0.20.4`, also have `PASSED` summaries, at `SLSA_BUILD_LEVEL_UNEVALUATED`.
Before that promotion pull request merges, the release manager confirms that its `pull-k8sio-cip` log reports no Kueue promoter policy violation.
If the log reports a violation, the release manager does not merge the pull request and fixes the digest first.
If a promotion nevertheless records a `FAILED` summary, the release manager reports it in the release tracking issue, and the policy stays in `warn` until a later promotion passes.
Under `require`, `pull-k8sio-cip` fails a promotion pull request that adds a digest without valid provenance, and the promoter does not promote that digest.
`require` checks only digests promoted after the switch.
The Security Profiles Operator moved its policy to `require` the same day its first release with provenance, v1.1.1, was promoted under `warn` without a violation.

### Release candidates

Release candidate tags trigger the same postsubmit build, so release candidates are attested on staging without extra configuration.
Release candidates are not promoted to the production registry, and this KEP does not change that; see [Alternatives](#alternatives).
Consumers verify release candidate images and charts on staging, as described in Stories 1 and 3, within the staging retention window described in [Promotion](#promotion).

### Release branches

Kueue maintains the two most recent minor releases and can tag patch releases from both release branches on the same day, for example v0.19.11 and v0.20.4.
Each tag build runs the staging build definition of its own commit, so a release branch without the attestation step pushes unattested artifacts.
Once the Kueue promoter policy exists, each of those artifacts gets a permanent `FAILED` verification summary under `warn`, and fails promotion under `require`.

The release managers backport the attestation in this order:

1. After the attestation step merges on `main`, cherry-pick the release runbook's "Staging artifact provenance v1" section, the attestation step and its self-check to every supported release branch, which already carries step 1 of [Build identity rollout](#build-identity-rollout).
   The section lands on each branch together with or before the attestation step, because the `buildType` URI resolves to the release runbook at the attested commit (see [Provenance content](#provenance-content)).
2. Wait until a staging build of each supported release branch passes its self-check and [policy dry-run test 5](#policy-dry-run-tests).
3. Merge the attestation wait and the ref and commit check in `/wait-for-images`, and the ref and commit check in the release promotion script.
   Both run the same check for releases from every branch, so an earlier merge blocks patch releases from a release branch without the attestation step.
4. After the `skip_gcb_builder_shim` change in [Build identity rollout](#build-identity-rollout) has landed, add the `warn` policy in kubernetes/k8s.io, before the first promotion of a release with staging attestations.

A release with staging attestations that must be promoted before the policy merges is promoted without provenance, as described in [Promotion](#promotion).

Release branches cut from `main` after the attestation step merges carry it already.
A release branch that reaches end of life before the backport does not get it.
It has no further patch releases, so it adds no digests to the promoter manifest.
Its promoted digests are not promoted again, so the policy does not block them; see [Verification summaries](#verification-summaries).
The policy does not wait for a passing promotion from every supported release branch before it moves to `require` (see [Alternatives](#alternatives)).

### Changes in this repository

| Change | Required for |
|---|---|
| The staging build definition sets `serviceAccount` to `kueue-sa`, on `main` and every supported release branch | Signing with the Kueue-specific identity; step 1 of [Build identity rollout](#build-identity-rollout) |
| The staging build definition adds an attestation build step after the image and chart pushes | [Attestation step](#attestation-step) |
| The build writes and signs a SLSA v1 predicate for each pushed artifact | Attestation step |
| The build verifies the attestations of all pushed artifacts before it completes (self-check) | Alpha graduation |
| The `/wait-for-images` release step also waits until `cosign verify-attestation` against the build identity passes for each staging image and chart digest, checks that each attestation records the release tag and the commit it points to, and accepts an option that skips both checks for a release built with `SIGN=false`, which the release ChatOps parser lets the release manager request; the job that runs it installs cosign v3.0.1 or later next to `crane`, through an action pinned by commit SHA | Opening the promotion pull request only after attestations for the release tag and its commit exist (see [Release-time attestation checks](#release-time-attestation-checks)); merged in the order in [Release branches](#release-branches) |
| The release promotion script verifies the attestation of each staging image and chart digest that it records against the build identity, runs the same ref and commit check, exits without opening the promotion pull request if any digest fails, and accepts an option that skips both for a release built with `SIGN=false`; it requires cosign v3.0.1 or later on the release manager's machine | Recording only digests whose attestations match the release tag and its commit, even if a tag moved after `/wait-for-images` (see [Release-time attestation checks](#release-time-attestation-checks)); merged in the order in [Release branches](#release-branches) |
| The release runbook gains a "Staging artifact provenance v1" section that defines the `buildType` URI | Resolving the `buildType` placeholder |
| Cherry-pick the "Staging artifact provenance v1" release runbook section, the attestation step and its self-check to every supported release branch | Alpha (see [Release branches](#release-branches)), before the `warn` policy is added |

The Security Profiles Operator (SPO) is the reference prototype: its staging build runs as the project's staging service account, attests each artifact after publishing it, and verifies the attestations before the build completes.
Kueue's implementation follows the same pattern, adapted to the Kueue build identity and artifacts.

### Changes in other repositories

| Repository | Change | Required for |
|---|---|---|
| kubernetes/test-infra | Set `serviceAccountName: kueue` in the staging build job definition | Step 2 of [Build identity rollout](#build-identity-rollout) |
| kubernetes/k8s.io | Add `kueue` to `skip_gcb_builder_shim` | Step 3 of [Build identity rollout](#build-identity-rollout) |
| kubernetes/k8s.io | Add the Kueue promoter policy in `warn` mode to the promoter manifest | Alpha graduation: carrying attestations to production (see [Release branches](#release-branches)) |
| kubernetes/k8s.io | Change the policy to `require` after the first Kueue promotion that passes it (see [Promotion](#promotion)) | Beta graduation |

These changes are not part of this KEP's implementation PR.
They are tracked as external follow-ups.
The Kueue promoter policy follows SPO's policy in kubernetes/k8s.io.
Its fields were confirmed against kpromo v4.8.0 and the [kpromo provenance policy documentation](https://github.com/kubernetes-sigs/promo-tools/blob/main/docs/image-promotion.md#provenance-policies):

```yaml
provenance:
  mode: warn  # changes to require after the first Kueue promotion that passes the policy
  signers:
  - sigstore::https://accounts.google.com::kueue-sa@k8s-staging-images.iam.gserviceaccount.com
  builders:
  - id: https://cloudbuild.googleapis.com/projects/k8s-staging-images/serviceAccounts/kueue-sa@k8s-staging-images.iam.gserviceaccount.com/cloudbuild.yaml
    level: 1
  sources:
  - github.com/kubernetes-sigs/kueue
```

The Kueue promoter manifest already lists exactly the 8 Phase 1 artifacts, so the policy needs no per-image scoping.
No pre-merge check evaluates the policy against already promoted digests.
The `pull-k8sio-cip` presubmit runs kpromo as a dry run on only the digests that a pull request adds.
A pull request that only adds the policy adds no digests, so `pull-k8sio-cip` does not evaluate the policy against any Kueue image.
In `warn` mode, `pull-k8sio-cip` logs each violation and passes.
Already promoted digests are not promoted again, so the policy does not block them; see [Verification summaries](#verification-summaries).
Before the `warn` policy merges, the [policy dry-run tests](#policy-dry-run-tests) exercise it on staging digests that are not yet promoted.
After it merges, the `pull-k8sio-cip` log of each Kueue promotion pull request reports the policy result for the digests that pull request adds.

### Interactions

- SBOM and OpenVEX release steps: unchanged. They run in a separate GitHub Actions workflow that publishes a CycloneDX SBOM and an OpenVEX document as GitHub release assets. These are separate from the OCI referrer attestations produced by this KEP and are not promoted to the production registry.
- Promotion signature: unchanged. It is added at copy time and exists alongside the build provenance.
- Periodic builds: not covered, because those images are not promoted.
- Consumer documentation and the release checklist: tracked by the docs sub-issue.
- GitHub release asset provenance: future work.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes necessary
to implement this enhancement.

#### Prerequisite testing updates

None.

#### Unit tests

Predicate generation has a unit-level script test that runs offline, without Cloud Build access or signing.
The test runs the local dry-run mode and checks the predicate JSON it writes: `externalParameters.source.uri` for a release tag, a release candidate tag and a branch ref, `gitCommit`, and `runDetails.builder.id`.
The implementation adds this test to `make verify`, so the `pull-kueue-verify-main` presubmit runs it on every pull request that changes the build scripts.
The verify presubmit of each release branch that carries the backport runs it there.

The ref and commit check, which `/wait-for-images` and the release promotion script share, has an offline unit test that uses predicate JSON fixtures instead of registry access and signing.
It checks that a release tag and an annotated release tag with their commits pass, and that a branch ref, another tag and another commit each fail.
It also checks that the options for a release built with `SIGN=false`, in `/wait-for-images` and in the release promotion script, skip the ref and commit check.
The implementation adds this test to `make verify` too, so the `pull-kueue-verify-main` presubmit runs it on every pull request that changes the release scripts.

#### Integration tests

Not applicable. This KEP adds no controller code.

#### e2e tests

Not applicable for automated CI. Coverage comes from:

1. The in-build self-check.
2. After merge, a manual `cosign verify-attestation` on the digest of every Phase 1 artifact in [Attested artifacts](#attested-artifacts) from the first staging build.
   Run it within 90 days of that staging build, the staging retention window described in [Promotion](#promotion); if the window has passed, trigger a fresh staging build and verify its digests instead.
3. The [policy dry-run tests](#policy-dry-run-tests) below, which cover the Kueue promoter policy in `warn` and `require` modes.
4. After the first Kueue promotion under `warn`, the release manager verifies that every index digest the promotion pull request added has a `PASSED` verification summary at `SLSA_BUILD_LEVEL_1`, before moving the policy to `require`.
   The release manager also runs `cosign verify-attestation` against `registry.k8s.io` on each of those digests.

##### Policy dry-run tests

A Kueue release manager runs these tests through the `pull-k8sio-cip` presubmit, which runs kpromo in dry-run mode and evaluates the Kueue promoter policy before it exits.
Each test uses a test pull request in kubernetes/k8s.io that adds the policy in the stated mode and the test digests to the Kueue promoter manifest; the pull request is closed without merging.
A dry run promotes nothing, so it records no `FAILED` verification summary on `registry.k8s.io`.
Tests 3 and 4 require a staging project administrator to push a specially prepared test image to a dedicated test image name in the Kueue staging repository before opening the test pull request.

1. Before the `warn` policy merges: a test pull request with the policy in `warn` mode and the digests of a staging image and a staging Helm chart that lack attestations (for example, builds from before the attestation step that are still within the 90-day staging window). Confirm that `pull-k8sio-cip` passes and its log reports the missing attestation for both.
2. Before the `warn` policy merges: the same test with the policy in `require` mode. Confirm that `pull-k8sio-cip` fails and names both artifacts.
3. Before the `warn` policy merges: a test pull request with the policy in `require` mode and the digest of a test image whose attestation bundle was altered after signing. Confirm that `pull-k8sio-cip` fails and names the image.
4. Before the `warn` policy merges: the same test as test 3, but with the attestation signed by an identity other than `kueue-sa` (for example, the administrator's own keyless identity through `cosign attest`). Confirm that `pull-k8sio-cip` fails and names the image.
5. Before the `warn` policy merges: a test pull request with the policy in `require` mode and the attested staging digests of every Phase 1 artifact from the latest build of `main` and of every supported release branch. Confirm that `pull-k8sio-cip` passes and its log reports no Kueue promoter policy violation.

### Graduation Criteria

#### Alpha

- The staging postsubmit attests every Phase 1 artifact in [Attested artifacts](#attested-artifacts) on every successful build that does not set `SIGN=false`.
- The in-build self-check passes.
- The local dry-run mode produces the predicate without signing.
- Blocking prerequisite: the release runbook has a "Staging artifact provenance v1" section that defines the `buildType` used in the predicate (see [Provenance content](#provenance-content)).
  This section merges before the attestation step is enabled in the staging build, so no attestation references an unresolvable `buildType` URI.
  It is tracked as a sub-task of the KEP implementation under kubernetes-sigs/kueue#1466 and must merge before the attestation step PR.
- The three steps of [Build identity rollout](#build-identity-rollout) have landed in the order listed there, each followed by a successful staging build of `main`.
  The kubernetes/test-infra change is tracked by a sub-issue of the KEP issue, kubernetes-sigs/kueue#1466.
- Every supported release branch carries the "Staging artifact provenance v1" release runbook section and the attestation step, and a staging build of each passes its self-check, as described in [Release branches](#release-branches).
- [Policy dry-run tests](#policy-dry-run-tests) 1 to 5 pass.
- The Kueue promoter policy in `warn` mode has merged in kubernetes/k8s.io before the first promotion of a release with staging attestations.
- The release checklist covers the order in [Release branches](#release-branches), the attestation wait and the ref and commit check in `/wait-for-images`, the ref and commit check in the release promotion script, and the `pull-k8sio-cip` log check in [Promotion](#promotion).
- Stories 1 to 3 are published as consumer instructions.

#### Beta

- One full release has verifiable provenance on the production registry for every Phase 1 artifact in [Attested artifacts](#attested-artifacts).
  Its promotion under `warn` passed the policy: kpromo recorded a `PASSED` verification summary with the verified level `SLSA_BUILD_LEVEL_1` for each of those artifacts.
- Consumer documentation has merged.
- The Kueue promoter policy is in `require` mode, switched after the first promotion that passed it, as described in [Promotion](#promotion).
- All bugs reported against the attestation step and the Kueue promoter policy are fixed.

#### Stable

- Every supported release branch has had a patch release promoted under `require`, and kpromo recorded a `PASSED` verification summary with the verified level `SLSA_BUILD_LEVEL_1` for every index digest that its promotion pull request added to the promoter manifest.
- All bugs reported against the attestation step and the Kueue promoter policy after Beta graduation are fixed.

## Implementation History

- 2026-10-26: Initial KEP draft.

## Drawbacks

- The build depends at runtime on the public Sigstore infrastructure (Fulcio and Rekor). A Sigstore outage can delay builds.
- Rekor entries are public. Build metadata (source URI, commit, and invocation ID) becomes visible to anyone.
- Each release branch needs one more build step to maintain.

## Alternatives

**Cloud Build native provenance.**
Cloud Build produces a KMS-signed attestation stored in Artifact Analysis, not as a Sigstore OCI referrer.
The image promoter does not read Artifact Analysis records, so the attestation would not reach the production registry.

**goreleaser.**
It replaces the image build tooling instead of adding to it.
This is out of scope.

**tejolote.**
It is an external observer that wraps build steps.
It adds operational complexity.
At Build L1, the simpler in-build pattern is sufficient.

**Building and attesting images in GitHub Actions.**
Kueue's images are built by Cloud Build, not GitHub Actions.
This would duplicate the multi-arch build in a different environment.

**Relying on the promotion signature only.**
The promotion signature confirms the copy path, not the build.
It cannot show which source commit produced the image.

**Attesting staging artifacts only in Alpha and adding the Kueue promoter policy at Beta.**
Alpha would attest staging artifacts without the Kueue promoter policy, to validate the attestations before production carries them.
Consumers would verify the staging artifact and check that its digest matches the production one before they install it.
Releases promoted during Alpha would keep an `SLSA_BUILD_LEVEL_UNEVALUATED` summary, and most would lose their attestations for good; see [Promotion](#promotion).
The [policy dry-run tests](#policy-dry-run-tests) validate the attestations against the policy without promoting anything, so a staging-only phase adds no check.
Kubernetes added its `warn` policy before the first promotion of its attested images, and the Security Profiles Operator added its policy the day kpromo first supported policies.

**Attesting by tag under the default build identity.**
The default build identity is shared by all projects in the staging GCP organization, not only Kueue.
A tag-based attestation is also not reproducible when the tag is reused across builds of the same commit.
It would also use SLSA v0.2 predicates, which are superseded.

**Promoting release candidates to the production registry.**
Release candidates would also carry provenance on `registry.k8s.io`, which needs the release promotion script to accept pre-release tags.
Provenance does not need this: release candidates are attested on staging.
The chart packaging points chart images at `registry.k8s.io` only for release tags without a pre-release suffix, so a promoted release candidate chart would reference staging images, which staging deletes after 90 days.
Promoting release candidates is also a release policy decision outside this KEP.

**Keeping the policy in `warn` until Stable, behind manual and scripted pre-promotion gates.**
The policy would stay in `warn` mode until Stable graduation.
Before each promotion, the release manager would run `cosign verify-attestation` on every staging digest and check its source ref and commit.
From Beta, the release promotion script would refuse to record a digest that failed these checks.
After each promotion, the release manager would review the verification summaries for unexpected `FAILED` results.
Each digest these checks missed would be promoted without provenance and keep a permanent `FAILED` summary.
Under `require`, `pull-k8sio-cip` fails a promotion pull request that adds a digest whose provenance does not match the Kueue signer, builder and source repository.
`/wait-for-images` and the release promotion script already reject an attestation signed for another ref or commit (see [Release-time attestation checks](#release-time-attestation-checks)), so the gates would only repeat existing checks.

**Waiting for a passing promotion from every supported release branch before `require`.**
This would keep the policy in `warn` for several patch releases.
Each of them that failed the policy would keep a permanent `FAILED` summary.
Under `require`, a release-branch digest that fails the policy is not promoted and gets no summary, so a broken backport delays that patch release instead.
[Policy dry-run test 5](#policy-dry-run-tests) checks the attestations of every supported release branch before the `warn` policy merges.

## Future work ideas

- Phase 2: provenance for GitHub release assets (`kubectl-kueue` binaries, rendered manifests, and Helm chart tarballs), using GitHub's native attestation API.
- SLSA Build L2 and L3. L2 requires hermetic builds (no network access during the build step); L3 additionally requires service-generated provenance (build steps cannot influence it), non-falsifiable provenance, and a separately-signed builder image. The Kubernetes release effort tracks these requirements in kubernetes/release#2616, which serves as the reference roadmap.
- Attesting periodic build images (debug, importer, ray-mini).
- Publishing SBOMs (CycloneDX) and OpenVEX documents as OCI referrers next to the provenance attestations, enabling cosign and kpromo to carry them through the same promotion path as build provenance.
  The OpenVEX output is already produced by the repository's existing OpenVEX workflow; attaching it as an OCI referrer is blocked on kubernetes-sigs/promo-tools#2059 (tracking the same path as kubernetes/release#4603).
  With SBOMs published this way, the `ClusterImagePolicy` in [Story 2](#story-2-a-platform-team-wants-to-reject-kueue-images-without-build-provenance-at-admission-time) could add an attestation entry with `predicateType: https://cyclonedx.org/bom`, which rejects any image that lacks either the provenance or the SBOM.

## Appendix: External terms

- **SLSA** (Supply-chain Levels for Software Artifacts): a framework that defines levels of build provenance assurance. This KEP targets SLSA Build L1, which requires provenance from the build system; this KEP also signs it.
- **Sigstore**: an open-source project providing tools and infrastructure for signing and verifying software artifacts using short-lived certificates and a public transparency log.
- **cosign**: a Sigstore tool for signing and verifying container images and other OCI artifacts using short-lived certificates and a public transparency log.
- **Fulcio / Rekor**: Sigstore components. Fulcio issues short-lived signing certificates tied to a workload identity (OIDC token). Rekor is the public transparency log where certificates and signatures are recorded and made auditable.
- **OCI referrer**: an artifact attached to an existing image or chart via the OCI referrers API, allowing tools to discover signatures, attestations, and SBOMs associated with a digest without modifying the original artifact.

## Appendix: Tool version requirements

Kueue publishes attestations only in the Sigstore bundle format, stored as OCI referrers.

- **cosign**: v3.0.1 or later reads bundle-format attestations by default (v3.0.0 was tagged but never released).
  With cosign v2.5.0 to v2.6.x, add `--new-bundle-format`.
  Earlier releases cannot verify bundle-format image attestations: cosign v2.4.0 added bundle support only for blob commands such as `verify-blob-attestation`, not for `verify-attestation` on images.
- **Sigstore Policy Controller**: releases before v0.13.0 do not understand `signatureFormat: bundle`.
  If the field is silently dropped, the Policy Controller falls back to its default (legacy-format) verification; because Kueue produces only bundle-format attestations, the legacy check finds none and rejects the image.
  If `kubectl apply` rejects the manifest outright, the policy is never created and no enforcement applies.
- **Helm**: Helm versions before 3.8.0 use experimental OCI commands and are not supported.
