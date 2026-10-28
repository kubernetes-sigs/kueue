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
  - [Attestation step](#attestation-step)
  - [Provenance content](#provenance-content)
  - [Failure handling](#failure-handling)
  - [Promotion](#promotion)
  - [Release candidates](#release-candidates)
  - [Changes in other repositories](#changes-in-other-repositories)
  - [Interactions](#interactions)
  - [Test Plan](#test-plan)
    - [Prerequisite testing updates](#prerequisite-testing-updates)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [e2e tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
    - [Alpha (v0.21)](#alpha-v021)
    - [Beta (v0.22)](#beta-v022)
    - [Stable (v0.23)](#stable-v023)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
- [Future work ideas](#future-work-ideas)
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

### Goals

- Every promoted image and Helm chart carries a Sigstore-signed SLSA v1 build provenance attestation on staging.
- The attestation survives promotion to the production registry.
- Release candidates are covered by the same mechanism.
- Consumers can verify provenance with a documented signing identity and issuer.
- A failed attestation step fails the build visibly.

### Non-Goals

- Provenance for GitHub release assets (`kubectl-kueue` binaries, rendered manifests, raw Helm chart tarballs). These are future work.
- SLSA Build Level 2 or higher.
- Changes to Kueue's Kubernetes APIs, configuration, or controller behavior.
- Attesting images that are not promoted to the production registry, such as periodic build images.
- Retroactive attestation of past releases.

## Proposal

The staging build signs build provenance for every release image and Helm chart.
The provenance stays with each artifact when the artifact is promoted to the production registry.
Consumers check the provenance against a documented Kueue build identity before they deploy Kueue.

### Terminology

- **SLSA** (Supply-chain Levels for Software Artifacts): a framework that defines levels of build provenance assurance. This KEP targets SLSA Build L1, which requires provenance from the build system; this KEP also signs it.
- **Sigstore**: an open-source project providing tools and infrastructure for signing and verifying software artifacts using short-lived certificates and a public transparency log.
- **Staging artifact**: an image or Helm chart published to the staging Artifact Registry by a postsubmit build.
- **Promoted artifact**: a staging artifact copied to the production registry by the image promoter (`kpromo`, from kubernetes-sigs/promo-tools).
- **Build provenance**: a signed statement of which source, build system, and invocation produced an artifact.
- **Promotion signature**: a signature added by the image promoter at copy time. It attests the copy, not the build.
- **Build identity** (`kueue-sa`): the service account that runs the staging build. Its email address is the Sigstore signing identity.
- **OCI referrer**: an artifact attached to an existing image or chart via the OCI referrers API, allowing tools to discover signatures, attestations, and SBOMs associated with a digest without modifying the original artifact.
- **cosign**: a Sigstore tool for signing and verifying container images and other OCI artifacts using short-lived certificates and a public transparency log.
- **Fulcio / Rekor**: Sigstore components. Fulcio issues short-lived signing certificates tied to a workload identity (OIDC token). Rekor is the public transparency log where certificates and signatures are recorded and made auditable.

### User Stories

#### Story 1: A cluster admin cannot tell which commit built a released Kueue image

An admin wants to verify the build provenance of a released Kueue image and confirm which commit built it.
They verify the attestation of the image digest and read the source commit from the predicate.
Use the multi-arch image index digest, not a platform manifest digest.
Resolve the release tag to its multi-arch index digest with `crane` (part of `go-containerregistry`):

```bash
crane digest registry.k8s.io/kueue/kueue:v<version>
# sha256:<multi-arch-index-digest>
```

During Alpha (v0.21), only staging images carry attestations; replace `registry.k8s.io/kueue/kueue` with `us-central1-docker.pkg.dev/k8s-staging-images/kueue/kueue` in the `crane` and `cosign` commands below.
From Beta (v0.22) onward, production images carry attestations and the command applies as written.

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
        "uri": "git+https://github.com/kubernetes-sigs/kueue@refs/tags/v0.21.0",
        "digest": { "gitCommit": "<commit-sha>" }
      }
    }
  },
  "runDetails": {
    "builder": { "id": "<cloud-build-platform-uri>" }
  }
}
```

This excerpt shows only the source commit and builder fields relevant to commit verification.
The full predicate shape, including `buildType`, `resolvedDependencies`, and invocation ID, is described in [Provenance content](#provenance-content).

If no attestation is present — because the image predates the feature or the build failed before the attestation step — `cosign` exits with a non-zero status and prints a message such as `no matching attestations`. This is expected for any Kueue image built before the v0.21 Alpha milestone or during an interrupted build.

#### Story 2: A platform team wants to reject Kueue images without build provenance at admission time

A platform team runs the [Sigstore Policy Controller](https://docs.sigstore.dev/policy-controller/overview/) in their clusters.
The `ClusterImagePolicy` below is a Policy Controller resource and has no effect without it.
It needs Policy Controller v0.13.0 or later, the first release that supports `signatureFormat: bundle`.
Earlier releases do not understand `signatureFormat: bundle`.
If the field is silently dropped, the Policy Controller falls back to its default (legacy-format) verification;
because Kueue produces only bundle-format attestations, the legacy check finds none and rejects the image.
If `kubectl apply` rejects the manifest outright, the policy is never created and no enforcement applies.
The `cosign verify-attestation` commands in Stories 1 and 3 need cosign v3.0.1 or later (v3.0.0 was tagged but never released), which reads bundle-format attestations by default.
With cosign v2.5.0 to v2.6.x, add `--new-bundle-format`.
Earlier releases cannot verify bundle-format image attestations: cosign v2.4.0 added bundle support only for blob commands such as `verify-blob-attestation`, not for `verify-attestation` on images.
The Policy Controller admits Kueue images only when a provenance attestation from the Kueue build identity is present.
When attestation verification fails — no matching provenance, wrong identity, or invalid signature — Policy Controller denies admission and returns a policy violation to the caller:

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

This policy applies from Beta (v0.22) when production images carry attestations.
During Alpha (v0.21), replace `registry.k8s.io/kueue/**` with
`us-central1-docker.pkg.dev/k8s-staging-images/kueue/**` to enforce attestations on
staging images; production images have no attestations during Alpha.

#### Story 3: An admin cannot tell which commit built the Kueue Helm chart they install

An admin installs Kueue from the Helm chart at `oci://registry.k8s.io/kueue/charts/kueue`.
Before installing, they want to confirm which commit built the chart.
`helm pull` on OCI charts requires Helm 3.8.0 or later; earlier versions use experimental OCI commands and are not supported.
Resolve the chart's OCI manifest digest with `crane` (part of `go-containerregistry`), the same tool used for image digests in Story 1; then pass that digest to `cosign`.

During Alpha (v0.21), only the staging chart carries an attestation; replace `registry.k8s.io/kueue/charts/kueue` with `us-central1-docker.pkg.dev/k8s-staging-images/kueue/charts/kueue` in the commands below.
From Beta (v0.22) onward, production charts carry attestations and the commands apply as written (unless chart carry is deferred; see [Beta](#beta-v022)).

```bash
crane digest registry.k8s.io/kueue/charts/kueue:<chart-version>
# sha256:<digest>

cosign verify-attestation \
  --type slsaprovenance1 \
  --certificate-identity kueue-sa@k8s-staging-images.iam.gserviceaccount.com \
  --certificate-oidc-issuer https://accounts.google.com \
  registry.k8s.io/kueue/charts/kueue@sha256:<digest>
```

The predicate has the same shape as in Story 1, with the chart digest as the attestation subject.
During Alpha (v0.21), only the staging chart carries an attestation.
Resolve the staging chart digest with `crane`, then run `cosign verify-attestation`:

```bash
crane digest us-central1-docker.pkg.dev/k8s-staging-images/kueue/charts/kueue:<chart-version>
# sha256:<digest>
```

Then run the `cosign verify-attestation` command above against `us-central1-docker.pkg.dev/k8s-staging-images/kueue/charts/kueue@sha256:<digest>`.

During Alpha, production carries no attestation, so confirm the production chart digest matches the verified staging digest before installing:

```bash
crane digest registry.k8s.io/kueue/charts/kueue:<chart-version>
# sha256:<same-digest>
```

Only install the chart if both digests are identical; otherwise the staging attestation does not cover what is installed.

A chart attestation does not cover the images that the chart deploys; verify those separately, as in Story 1.

### Notes/Constraints/Caveats

- The level achieved is SLSA Build L1.
  The build writes its own provenance predicate, and build steps are not isolated from each other.
- Attestations attach to the multi-arch index digest that the image promoter references.
  Platform-specific manifests do not carry their own attestation.

- During Alpha (v0.21), attestations are attached to staging artifacts only.
  Production-registry (`registry.k8s.io`) images do not carry attestations until the promoter policy change in `kubernetes/k8s.io` lands (Beta).

### Risks and Mitigations

#### A Sigstore outage can delay staging builds and releases

A staging build cannot sign or verify while Sigstore is unavailable.
Rerun the postsubmit after the outage.
Under `warn`, the promoter does not reject an unattested artifact automatically.
The release manager still holds the promoter manifest pull request until the attestation is present and verified, as described in [Failure handling](#failure-handling).

#### Anyone who can act as the build identity can sign false provenance

Access to the service account is limited to staging project administrators and the Cloud Build pipeline.
Consumers must pin both the signing identity and the issuer when they verify.
Consumers must not treat L1 provenance as tamper-proof.

#### An unattested artifact can be promoted during the transition period

The promoter policy starts in warn mode, so an unattested artifact can still be promoted.
Until the policy requires provenance, the in-build self-check fails the build visibly if attestation fails.
The release manager must still verify the attestation of each digest recorded in the promoter manifest pull request before it merges, as described in [Failure handling](#failure-handling).

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

### Build identity

The staging build runs as a dedicated service account, `kueue-sa`, in the staging project.
The account already has Artifact Registry write access and OIDC token-creator permissions.
The build obtains a Fulcio identity certificate from its ambient Google identity, so it needs no extra credentials.
Consumers verify against the account's email address and the Google OIDC issuer.

### Attestation step

1. After each image and chart push completes, record its digest from the push command's own output — not by re-resolving the tag afterward.
   `docker push` and `crane push` both print the pushed digest on stdout; capture that value directly.
   For Helm OCI charts, `helm push oci://...` prints `Digest: sha256:<digest>` on stdout; capture that value the same way.
   Re-resolving by tag is unsafe: if the tag moves between the push and the resolution (for example, because a concurrent build force-pushes the same tag), the build would attest the wrong digest.
   The `vX.Y.Z` tag (for images) and `X.Y.Z` version without leading `v` (for charts) are used only as the target reference for the push itself; the digest passed to subsequent steps must come from the push output.
2. Write a SLSA v1 predicate that records the source repository, git ref, git commit, builder identity, and build invocation ID.
   The git ref and commit SHA come from the Cloud Build trigger payload, which records the triggering tag ref (`refs/tags/vX.Y.Z`) and the resolved commit SHA.
   Use the tag ref, not a mutable branch ref, so the source reference uniquely identifies the commit.
3. Run a pinned Cosign version (v3.0.1+; or v2.5.x–v2.6.x with `--new-bundle-format`) to attach
   the predicate as a Sigstore bundle OCI referrer, using the build's ambient workload identity.
   Pass `artifact@sha256:<digest>`, the digest resolved in Step 1, not the tag reference.
   A tag can move between steps, so attaching by tag could attach the attestation to a different push.
4. Verify the attestation against the same `artifact@sha256:<digest>` from Step 1 and the build identity, as an in-build self-check.
   Fail the build if verification fails.

A local dry-run mode writes the predicate without signing.

### Provenance content

The predicate has this shape. Values in angle brackets vary per build.
The `builder.id` URI is a placeholder; per SLSA v1, it must identify the build platform (the transitive closure of the trusted builder), not the IAM service account. The exact URI must be confirmed from Cloud Build's provenance documentation before the attestation step is enabled; this is a blocking Alpha prerequisite (see [Alpha](#alpha-v021)). The service account email (`kueue-sa@k8s-staging-images.iam.gserviceaccount.com`) belongs only in the Fulcio certificate identity fields used for verification.

**Note:** This predicate is provisional. The `buildType` URI is a placeholder that does not resolve until the Alpha implementation prerequisite — adding the `Staging artifact provenance v1` section to `RELEASE.md`, which documents the build inputs and outputs for images and Helm charts — has merged (see [Alpha](#alpha-v021)). That section is tracked as a sub-task of the KEP implementation under kubernetes-sigs/kueue#1466.

```json
{
  "buildDefinition": {
    "buildType": "https://github.com/kubernetes-sigs/kueue/RELEASE.md#staging-artifact-provenance-v1",
    "externalParameters": {
      "source": {
        "uri": "git+https://github.com/kubernetes-sigs/kueue@refs/tags/v0.21.0",
        "digest": { "gitCommit": "<commit-sha>" }
      }
    },
    "resolvedDependencies": [
      { "uri": "git+https://github.com/kubernetes-sigs/kueue", "digest": { "gitCommit": "<commit-sha>" } }
    ]
  },
  "runDetails": {
    "builder": { "id": "<cloud-build-platform-uri>" },
    "metadata": { "invocationId": "https://console.cloud.google.com/cloud-build/builds/<build-id>?project=<project-number>" }
  }
}
```

`externalParameters.source` records the user-supplied tagged input (the git ref); `resolvedDependencies` records the resolved, pinned dependency (the specific commit). Both reference the same underlying commit SHA for a tag build.

### Failure handling

A failed attestation or self-check step fails the build with a non-zero exit code.
If Step 3 (attachment) fails, the artifact has no attestation in the staging registry.
If Step 4 (self-check) fails, the attestation is present but unverified; the release manager must treat it as unusable and must not promote that digest.
In either case, rerunning the postsubmit signs the artifact again.
The earlier push stays in the registry.
The release promotion script resolves each release tag to a digest once, and records that digest in the promoter manifest pull request.
`kpromo` promotes exactly the recorded digests; it does not resolve tags again.
If the tag still points to the unattested push when the script runs, that unattested digest is promoted.
Under `warn` mode, the release manager must:

1. Wait until the rerun postsubmit, including its self-check, completes before running the release promotion script.
2. Before the promoter manifest pull request merges, run `cosign verify-attestation` on every digest recorded in it, as `<staging-artifact>@sha256:<digest>`, not on the tag.
   Promotion then uses the same digest that was verified, so moving the tag after verification cannot change what is promoted.

Under `require` mode, the promoter enforces this automatically: it rejects a digest that has no valid attestation.

### Promotion

Once a `kpromo` release that copies OCI referrers is deployed, it will verify the build attestation when present and carry it to the production registry as an OCI referrer.
For container images, this requires that `kpromo` release.
For Helm OCI charts, if no such `kpromo` version is available by Beta, chart attestation carry is deferred per the [Beta graduation criteria](#beta-v022); images-only attestation on the production registry satisfies Beta in that case.
The minimum `kpromo` version for container images is not yet known; it must be confirmed from the `kpromo` release notes before Beta (see [Beta](#beta-v022)).
Carrying the attestation also requires the Kueue provenance policy in the promoter manifest in kubernetes/k8s.io (see [Changes in other repositories](#changes-in-other-repositories)). That change has not landed yet.
The promotion signature is added after the copy and is unaffected.
The promoter policy for Kueue starts in `warn` mode.
After one full release cycle, the policy moves to `require`.

### Release candidates

Release candidate tags trigger the same postsubmit build.
Release candidates are attested on staging without extra configuration.
Currently, the release promotion script accepts only `vX.Y.Z` versions and rejects `vX.Y.Z-rc.N` tags, so release candidates are not promoted to the production registry.
The script must accept release candidate tags before any release candidate can carry provenance on the production registry.
Until then, consumers verify release candidate images and charts on staging, as in the Alpha instructions of Stories 1 and 3.

### Changes in other repositories

| Repository | Change | Required for |
|---|---|---|
| kubernetes/test-infra | Submit the postsubmit build as `kueue-sa` | Signing with the Kueue-specific identity |
| kubernetes/k8s.io | Add a Kueue provenance policy in `warn` mode to the promoter manifest | Carrying attestations to production |
| kubernetes/k8s.io | Change the policy to `require` after one release | Stable graduation |

These changes are not part of this KEP's implementation PR.
They are tracked as external follow-ups.
The exact format and field names of the promoter manifest policy entry must be confirmed from the `kpromo` source or documentation at the time the kubernetes/k8s.io change is implemented; `warn` and `require` are the two supported modes.

### Interactions

- SBOM and OpenVEX release steps: unchanged. They run in a separate GitHub Actions workflow.
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

Predicate generation is tested offline.
The local dry-run mode writes the predicate JSON without signing, so it can be checked without Cloud Build access.

#### Integration tests

Not applicable. This KEP adds no controller code.

#### e2e tests

Not applicable for automated CI. Coverage comes from:

1. The in-build self-check.
2. After merge, a manual `cosign verify-attestation` on the digest of each artifact from the first staging build: the 5 images (`kueue`, `kueueviz-backend`, `kueueviz-frontend`, `kueue-populator`, `kueue-priority-booster`) and the 3 Helm charts (`kueue`, `kueue-populator`, `kueue-priority-booster`).
3. A check on the first promoted release candidate.
4. A manual promotion attempt with a staging image and, separately, a staging Helm chart, each lacking an attestation, under `warn` mode: confirm the promoter logs the missing attestation and completes promotion for both.
5. A manual promotion attempt with a staging image and, separately, a staging Helm chart, each lacking an attestation, under `require` mode: confirm the promoter rejects each artifact and does not promote it.
6. A manual promotion attempt under `require` mode with a staging image whose attestation has an invalid signature: confirm the promoter rejects it and does not promote it.
7. A manual promotion attempt under `require` mode with a staging image whose attestation is signed by an identity other than the Kueue build identity: confirm the promoter rejects it and does not promote it.
   This test requires the promoter policy format to define the expected signer identity; it can only be executed after the `kpromo` policy format is confirmed (see [Beta graduation criteria](#beta-v022)).

### Graduation Criteria

#### Alpha (v0.21)

- The staging postsubmit attests all 8 artifacts (5 images and 3 Helm charts) on every successful build.
- The in-build self-check passes.
- The local dry-run mode produces the predicate without signing.
- Blocking prerequisite: `RELEASE.md` has a "Staging artifact provenance v1" section that defines the `buildType` used in the predicate (see [Provenance content](#provenance-content)).
  This section merges before the attestation step is enabled in the staging build, so no attestation references an unresolvable `buildType` URI.
  It is tracked as a sub-task of the KEP implementation under kubernetes-sigs/kueue#1466 and must merge before the attestation step PR.
- Blocking prerequisite: the Cloud Build platform URI used as `runDetails.builder.id` is confirmed from Cloud Build's provenance documentation and recorded in the `RELEASE.md` "Staging artifact provenance v1" section (see [Provenance content](#provenance-content)).
  The attestation step is not enabled in the staging build while the predicate still contains the `<cloud-build-platform-uri>` placeholder, so no attestation records an unconfirmed builder identity.
- The kubernetes/test-infra change that submits the build as `kueue-sa` has landed. It is tracked by a sub-issue of the KEP issue, kubernetes-sigs/kueue#1466.

#### Beta (v0.22)

- Blocking prerequisite: a `kpromo` version that copies OCI referrers for container images has been confirmed from its release notes and is deployed in the production promotion pipeline.
  If no `kpromo` release copies OCI referrers for Helm OCI charts by v0.22, Helm chart attestation carry moves to Stable (v0.23).
  In that case, images-only attestation on the production registry satisfies the Beta criteria for this milestone.
- One full release has verifiable provenance on the production registry for all 5 images (`kueue`, `kueueviz-backend`, `kueueviz-frontend`, `kueue-populator`, `kueue-priority-booster`) and all 3 Helm charts (`kueue`, `kueue-populator`, `kueue-priority-booster`), or for the 5 images only if Helm chart carry is deferred as described above.
- At least one release candidate has verifiable provenance on the production registry.
  This requires the release promotion script to support release candidate tags (see [Release candidates](#release-candidates)).
  If that script change has not landed by v0.22, this criterion carries over to Stable (v0.23).
- Consumer documentation and the release checklist update have merged.
- The promoter policy for Kueue is in `warn` mode.

#### Stable (v0.23)

- The promoter policy for Kueue requires provenance.
- The policy has been in `require` mode for at least one full release cycle with no promotion failures attributable to the attestation pipeline.
- That release has verifiable provenance on the production registry for all 5 images (`kueue`, `kueueviz-backend`, `kueueviz-frontend`, `kueue-populator`, `kueue-priority-booster`) and all 3 Helm charts (`kueue`, `kueue-populator`, `kueue-priority-booster`).
- At least one release candidate has verifiable provenance on the production registry (carry-over from Beta if not met by v0.22; requires the release promotion script to accept `vX.Y.Z-rc.N` tags, see [Release candidates](#release-candidates)).

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

**Attesting by tag under the default build identity.**
The default build identity is shared by all projects in the staging GCP organization, not only Kueue.
A tag-based attestation is also not reproducible when the tag is reused across builds of the same commit.
It would also use SLSA v0.2 predicates, which are superseded.

## Future work ideas

- Phase 2: provenance for GitHub release assets (`kubectl-kueue` binaries, rendered manifests, and Helm chart tarballs), using GitHub's native attestation API.
- SLSA Build L2 and L3, through hermetic builds or a dedicated signing service.
- Attesting periodic build images (debug, importer, ray-mini).
- Publishing SBOMs as OCI referrers next to the provenance attestations.
