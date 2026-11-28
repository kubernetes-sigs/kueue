# KEP-NNNN: MultiKueue Authenticated Remote-Object Provenance

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
  - [Goals](#goals)
  - [Non-Goals](#non-goals)
- [Proposal](#proposal)
  - [User Stories](#user-stories)
    - [Shared worker Namespace](#shared-worker-namespace)
    - [Fail-closed recovery and rotation](#fail-closed-recovery-and-rotation)
  - [Dependencies and status](#dependencies-and-status)
  - [Risks and Mitigations](#risks-and-mitigations)
- [Design Details](#design-details)
  - [Goals, non-goals, and threat model](#goals-non-goals-and-threat-model)
  - [Signed claims envelope](#signed-claims-envelope)
  - [Object roles and shared controllers](#object-roles-and-shared-controllers)
  - [Creation, reassociation, and garbage collection](#creation-reassociation-and-garbage-collection)
  - [Configuration, rollout, and rotation](#configuration-rollout-and-rotation)
  - [Test Plan](#test-plan)
    - [Unit tests](#unit-tests)
    - [Integration tests](#integration-tests)
    - [E2E tests](#e2e-tests)
  - [Graduation Criteria](#graduation-criteria)
- [Implementation History](#implementation-history)
- [Drawbacks](#drawbacks)
- [Alternatives](#alternatives)
<!-- /toc -->

## Summary

MultiKueue currently identifies worker-cluster Workloads and execution objects
with public metadata. A worker principal that can read and mutate those objects,
including by patching or replacing them, can copy the metadata and induce
cross-object status adoption or confused-deputy cleanup. This proposal adds a
manager-keyed HMAC over a bounded, self-contained identity-claims envelope tied
to the exact worker UID.

The feature is opt-in, fails closed in enforcement mode, supports PodGroups and
shared multi-Workload controllers, and retains independently verifiable evidence
for exact-UID garbage collection after manager Workload state disappears.

## Motivation

The exact-UID and guarded-client hardening in #14753 and #14761 prevents
accidental collisions and validation-to-deletion races, but its labels and
annotations remain forgeable by a worker principal allowed to copy public
metadata. Operators who share worker Namespaces with such principals need
cryptographic evidence that the manager created the exact object being trusted.

### Goals

- Authenticate the provenance and MultiKueue association of every remote
  Workload and adapter-declared execution object.
- Reject pre-created and replacement objects without trusting public metadata.
- Support single-Workload objects, every PodGroup member, and shared controllers.
- Preserve enough signed evidence for exact-UID cleanup after manager object
  deletion.
- Define fail-closed bootstrap, upgrade, key rotation, audit, and test contracts.

### Non-Goals

- Prevent a worker principal from deleting, running, or denying service to its
  own objects.
- Provide cryptographic integrity for same-UID spec history or status.
- Contain compromise of the manager, signing key, or worker credential.
- Automatically adopt or delete an unsigned object after an ambiguous Create.

## Proposal

The manager stores a random HMAC keyring in its own Namespace. For each remote
object, it writes signed identity claims containing the manager, worker cluster,
object role, association, and exact worker UID. The manager authenticates those
claims at every status, mutation, and cleanup boundary.

### User Stories

#### Shared worker Namespace

As an operator, I can let a namespaced worker user create and replace Jobs
without allowing copied MultiKueue labels to make the manager adopt or delete
that user's object.

#### Fail-closed recovery and rotation

As an operator, I can audit legacy objects, enable enforcement, rotate keys
across replicas and workers, and clean up exact authenticated UIDs without
falling back to public metadata.

### Dependencies and status

This is a design-only Alpha proposal and depends on #14753 and #14761. Those
prerequisites must merge before implementation. The KEP number is left as
NNNN until the project assigns one; this KEP has its own metadata and lifecycle
rather than changing the Beta stage of KEP-693.

### Risks and Mitigations

Initial proof bootstrap necessarily spans multiple requests because the worker
UID is assigned by the API server. Proof-pending admission and execution
barriers prevent honest
partial failures from consuming quota or running, while ambiguous unsigned
objects block redispatch for operator review. Key loss prevents verification
and cleanup, so rotation retains verification-only keys until a complete live
inventory proves retirement safe. Enforcement excludes adapters without a
manager-owned bootstrap plan, a non-runnable barrier, and a protected status
subresource.

## Design Details

This is a design-only proposal. It depends on the exact-UID and guarded-client
hardening in #14753 and #14761, which is not present in the base snapshot of
this KEP change. The prerequisite series must merge first; the statements below
describe the proposed post-prerequisite design, not current released behavior.

The prerequisite work binds remote objects with public labels, annotations, and
Kubernetes UIDs. Those checks prevent accidental name collisions and
validation-to-deletion races, but public metadata is not proof of origin: a
worker principal that can read and mutate objects can copy every public value
to a replacement. Authenticated provenance adds a manager-generated message
authentication code (MAC) over a self-contained identity-claims envelope. The
key remains in the manager cluster.

### Goals, non-goals, and threat model

The attacker may have namespaced read, create, update, patch, and delete access
to supported objects in a worker cluster. The attacker knows every public
MultiKueue value and every earlier claims envelope, but cannot read the manager
key Secret, mutate manager-cluster Kueue state, forge Kubernetes UIDs, or
compromise the manager controller or its worker credential. Status is trusted
only for kinds whose status subresource is independently protected from this
attacker.

In `Enforce` mode, the design must provide these properties:

* A pre-created same-name object is never adopted for status relay, mutation,
  or deletion.
* A replacement receives a new worker UID, so copied claims and proof fail.
* Proofs cannot be replayed across clusters, object roles, GVKs, Namespaces,
  names, Workloads, controllers, or PodGroup members.
* With live manager state, authenticated claims must also equal the current
  manager-authorized association before status relay or a write.
* Manager deletes remain bound to the authenticated worker UID and use an API
  server UID precondition.
* When manager Workload or controller state is gone, the self-contained claims
  can still authenticate historical manager creation of the exact worker UID
  for fail-closed garbage collection.
* Missing, partial, malformed, unknown-version, unknown-key, or invalid proofs
  fail closed. A signed object never falls back to public metadata.

The proof authenticates object provenance and association, not the complete
execution history. Same-UID spec changes, temporary spec changes later restored,
status forgery, deletion, and denial of service by a worker mutator are
non-goals. Existing UID/resourceVersion guards still constrain manager writes,
but cryptographic spec integrity would require a separate per-adapter canonical
spec contract. Compromise of the manager, key, or worker credential is also out
of scope.

### Signed claims envelope

Three annotations are added to every authenticated remote object:

* `kueue.x-k8s.io/multikueue-identity-key-id` selects a verification key;
* `kueue.x-k8s.io/multikueue-identity-claims` contains base64url-encoded claims;
* `kueue.x-k8s.io/multikueue-identity-proof` contains a base64url-encoded
  HMAC-SHA-256 tag.

Base64url encoding is canonical and omits padding; decoders reject padding and
alternate encodings. The proof decodes to exactly 32 bytes. The decoded claims
are at most 8 KiB. Version 1 is a fixed-order binary record: a one-byte format
version followed by exactly 22 scalar fields. Each scalar is a one-byte presence
marker and a four-byte
unsigned big-endian byte length followed by that many UTF-8 bytes. A required
field uses presence value 1. A forbidden field uses presence value 0 and length
zero; it has no payload. Every scalar payload is at most 1 KiB. Except for a
Kubernetes core API group, required values are nonempty.

The role abbreviations in the wire matrix are `W` (`workload`), `C`
(`controller`), `P` (`pod-group-member`), `CW` (`component-workload`), and `S`
(`shared-controller`). `R` means required and `F` means forbidden.

| Index | Scalar field | W | C | P | CW | S |
| ---: | --- | :---: | :---: | :---: | :---: | :---: |
| 1 | signed key ID | R | R | R | R | R |
| 2 | configured MultiKueue origin | R | R | R | R | R |
| 3 | manager-side `MultiKueueCluster` name | R | R | R | R | R |
| 4 | manager-side `MultiKueueCluster` UID | R | R | R | R | R |
| 5 | role | R | R | R | R | R |
| 6 | remote API group | R | R | R | R | R |
| 7 | remote API version | R | R | R | R | R |
| 8 | remote kind | R | R | R | R | R |
| 9 | remote Namespace | R | R | R | R | R |
| 10 | remote name | R | R | R | R | R |
| 11 | remote worker UID | R | R | R | R | R |
| 12 | manager Workload Namespace | R | R | R | R | F |
| 13 | manager Workload name | R | R | R | R | F |
| 14 | manager Workload UID | R | R | R | R | F |
| 15 | manager execution-object API group | R | R | R | R | R |
| 16 | manager execution-object API version | R | R | R | R | R |
| 17 | manager execution-object kind | R | R | R | R | R |
| 18 | manager execution-object Namespace | R | R | R | R | R |
| 19 | manager execution-object name | R | R | R | R | R |
| 20 | manager execution-object UID | R | R | R | R | R |
| 21 | prebuilt Workload association | F | R | R | F | F |
| 22 | authenticated remote shared-controller UID | F | F | F | R | F |

The format version is the byte `0x01`; it is not a framed scalar. The signed key
ID is the canonical 22-character derived ID defined below. Role field 5 must be
one of the five exact strings above. API group fields 6 and 15 are present even
for core resources, where their canonical payload is empty. All other required
payloads are nonempty and contain the exact Kubernetes string representation;
UIDs are encoded as the exact case-sensitive textual values returned by the API
server, without normalization.
`workload` is the ordinary remote Workload form. `component-workload` is the
remote Workload form for a multi-Workload adapter and is the only form that can
carry a shared-controller UID.

Decoders reject invalid UTF-8, noncanonical presence values, a forbidden field
with a payload, a missing required field, an invalid role/presence combination,
lengths above the remaining input, trailing bytes, a scalar over 1 KiB, or an
envelope over 8 KiB. There are no maps, numbers, delimiter-joined values, or
role-specific extensions in version 1.

The MAC input is the ASCII domain separator
`kueue.multikueue.remote-identity/v1`, a zero byte, and the exact decoded claims
bytes. The external key-ID annotation must equal the signed key ID. Verification
uses `hmac.Equal`, authenticates the envelope first, and then compares every
object-derived GVK/name/Namespace/UID and public association value with its
claim. Public metadata remains useful for list selectors but never grants
authority.

All non-object-reconstructible data is inside the authenticated envelope. Thus
"manager state is gone" means only the manager Workload or controller is gone;
verification still requires the keyring, proof-version decoder, worker
credentials, persisted identity-type registry, and the current
`MultiKueueCluster` context. Deleting and recreating a `MultiKueueCluster`
changes its UID and intentionally makes old proofs ineligible for automatic
cleanup by the new object. They are preserved for operator review.

Version 1 golden vectors cover every valid role form. Rejection vectors cover
every missing required field, every populated forbidden field, and every
cross-role combination. Fuzzing asserts deterministic encoding, decode/encode
identity, field-boundary separation, rejection of noncanonical encodings, and a
different tag for every single-field mutation. Decoders, type descriptors, and
verification keys remain available until an inventory proves that no live
object uses them.

### Object roles and shared controllers

`workload`, `controller`, `pod-group-member`, and `component-workload` claims
bind one manager Workload and the exact manager execution-object identity. A
PodGroup member uses its own manager Pod UID, never the anchor UID.

`shared-controller` is for `MultiKueueMultiWorkloadAdapter` implementations such
as LeaderWorkerSet. It binds the stable manager controller and remote controller
UID but not one component Workload. Each component remote Workload is signed
independently and records the shared-controller UID in its claims. Garbage
collection can delete a shared controller only after verifying that no
authenticated component Workload on that worker still references it and that
the manager controller is absent or awaiting deletion. One missing component
Workload never authorizes deletion of the shared owner.

Custom adapters must declare every enumerable item/list GVK and allowed role
and provide a proof-pending execution barrier. An adapter whose status is not
independently protected from the worker attacker or whose object cannot be held
non-runnable is not eligible for enforcement mode.

The integration is additive and manager-owned. A proof-capable adapter
implements the following planning interface; it never receives a raw worker
client for bootstrap:

```go
type MultiKueueRemoteIdentityAdapter interface {
    MultiKueueAdapter
    RemoteIdentityObjectTypes() []RemoteIdentityObjectType
    PlanProofPendingRemoteObjects(
        ctx context.Context,
        localClient client.Client,
        key types.NamespacedName,
        workload *kueue.Workload,
        origin string,
    ) ([]RemoteObjectPlan, error)
    ActivateRemoteObject(
        ctx context.Context,
        authenticatedClient client.Client,
        plan RemoteObjectPlan,
    ) error
}

type RemoteObjectIdentityRole string

const (
    RemoteObjectIdentityWorkload RemoteObjectIdentityRole = "workload"
    RemoteObjectIdentityController RemoteObjectIdentityRole = "controller"
    RemoteObjectIdentityPodGroupMember RemoteObjectIdentityRole = "pod-group-member"
    RemoteObjectIdentityComponentWorkload RemoteObjectIdentityRole = "component-workload"
    RemoteObjectIdentitySharedController RemoteObjectIdentityRole = "shared-controller"
)

type RemoteIdentityObjectType struct {
    ItemGVK schema.GroupVersionKind
    ListGVK schema.GroupVersionKind
    AllowedRoles []RemoteObjectIdentityRole
}

type RemoteManagerObjectReference struct {
    GVK schema.GroupVersionKind
    Key types.NamespacedName
}

type RemoteObjectPlan struct {
    Object client.Object
    Role RemoteObjectIdentityRole
    ManagerObject RemoteManagerObjectReference
}
```

The manager provenance layer, not the adapter, is the identity authority. The
adapter returns only a logical manager GVK/key and a proof-pending remote
object. It cannot return a Workload, manager-object, shared-controller, or
worker UID. Before Create, the layer requires the plan GVK and role to match one
declared descriptor; derives the Workload and manager-object UIDs from the
freshly API-read Workload used by #14761; and cross-checks the exact same-name
manager object through the uncached API reader. A stale cached object or a
logical reference absent from the authoritative Workload is rejected.

Adapters may emit only `controller`, `pod-group-member`, or
`shared-controller` plans. `Object`, `Role`, and `ManagerObject` are required for
each plan, and no undeclared role/GVK or extra identity input is accepted. The
manager core creates `workload` and `component-workload` objects itself. It
populates those claims from the authoritative Workload and, for a component,
from the re-read and verified remote shared controller; adapters never supply
field 22. PodGroup plans enumerate every member, and a shared-controller plan is
emitted only once for its stable manager controller identity.

The manager layer executes Create, proof Patch, verification, and activation
for each plan and returns only authenticated objects. After bootstrap,
`SyncJob` and cleanup receive a client that composes provenance enforcement
with the identity guard from #14761; Create is rejected there so an adapter
cannot bypass bootstrap.

The core Workload descriptor and every normalized adapter item/list GVK and
allowed-role set are persisted in a manager-side ConfigMap in the controller
Namespace. Each registry version is an immutable, content-addressed ConfigMap;
configuration pins its exact name and full content digest. The inventory
registry is append-only until the leader stops new
dispatch for a retiring descriptor and a fresh, complete all-worker inventory
proves that no unsigned, partial, invalid, unknown-key, or valid object remains
for it. Inventory uses the union of persisted and currently registered
descriptors and can list a retained custom GVK as unstructured data even when
its adapter implementation is no longer active. A missing, corrupt, conflicting,
or stale registry fails closed and makes affected AdmissionChecks inactive;
configuration or binary changes cannot silently forget a GVK. Registry changes
create a new immutable snapshot and advance the configured name-and-digest pin
only through the transition barrier described below. A controller never mutates
an existing registry snapshot.

Registry version 1 stores a monotonically increasing generation and the
previous snapshot's 32-byte digest (all zeroes for the initial snapshot), plus
the descriptor tuples. Each tuple is normalized by item group/version/kind, list
group/version/kind, and a lexicographically sorted duplicate-free role set;
tuples are then sorted lexicographically and duplicates or conflicting list
GVKs are rejected. The registry digest is SHA-256 over a version byte, a
big-endian uint64 generation, the previous digest, a big-endian uint32 tuple
count, and the descriptor UTF-8 strings encoded with the same length framing as
claims. The active registry digest uses the same encoding with generation zero
and an all-zero previous digest. This makes controller, command, and machine-output
comparisons deterministic without trusting map or adapter registration order.
The ConfigMap has `immutable: true` and is named
`<validated-prefix>-<64-lowercase-hex-digest>`; configuration also carries that
full digest, so the name is not the only collision check. The controller reads
the snapshot through an uncached API reader, requires the configured name,
configured digest, recomputed payload digest, name suffix, immutable bit, and
predecessor link to agree, and otherwise keeps the AdmissionCheck inactive.
The digest and backup cover the canonical registry payload, not API-assigned
ConfigMap metadata such as UID or resourceVersion.

The registry has an explicit first-install ceremony. While provenance
configuration is still nil and before any proof-capable dispatch,
`kueuectl multikueue identity init-registry --name-prefix PREFIX` creates the
version-1 immutable ConfigMap from the current core and adapter descriptors
using an API Create. The command validates that the prefix and resulting full
digest name fit Kubernetes naming constraints.
The operation is idempotent only when an existing ConfigMap has identical bytes;
it never overwrites or widens different state. The resulting versioned registry
payload is backed up before enabling `Audit`, and non-nil configuration pins
its exact ConfigMap name and full digest.
Controllers never create or reconstruct a missing registry from active adapters.
After provenance has been enabled, deletion or loss is fatal and recovery
requires restoring the exact snapshot named by the current configured pin from
backup with an API Create; the replacement ConfigMap UID can differ. An older
but structurally valid backup has a different content-addressed name and digest
and is rejected after restart. It cannot be restored under the current name
because the recomputed digest and name suffix do not match. Re-running
initialization from active adapters is not a supported recovery path.

Every append or removal creates a successor with generation incremented by one
and `previousDigest` equal to the currently configured snapshot. The successor
is API-created as immutable, re-read and verified, durably backed up, and only
then proposed as the new configuration pin. The supported
`kueuectl multikueue identity update-registry` workflow requires name-prefix
and backup-file arguments, and writes and fsyncs the exact successor backup
before it emits the configuration stanza. A backup-write failure emits no
candidate pin and leaves the configured snapshot unchanged. The transition
barrier inventories the union needed by the old and successor registries before
accepting that pin;
all replicas must roll to the same pin before dispatch resumes. A concurrent
different successor, a generation skip, a predecessor mismatch, a missing
configured pin advancement, or a mix of old and new pins fails closed. Superseded
snapshots and their backups are retained for recovery and audit. Rolling back
the deployment configuration pin itself is a privileged trust-anchor rollback,
not registry recovery, and is unsupported while signed objects exist.

With nil configuration, existing adapters retain legacy behavior. In `Audit`,
an adapter lacking this interface continues with prerequisite public-identity
guards but is reported as `UnsupportedAdapter`; it receives no provenance
guarantee. In `Enforce`, any selected adapter that lacks the interface or a
valid barrier makes the affected AdmissionCheck inactive before dispatch.

### Creation, reassociation, and garbage collection

Remote UIDs do not exist before creation, so initial proof installation is a
multi-request protocol:

1. The manager creates the remote Workload or execution object in a non-runnable
   proof-pending state. A remote Workload is created with `spec.active=false`,
   so it cannot reserve quota or trigger preemption. Jobs remain suspended, Pods
   retain a scheduling gate, and custom adapters must provide an equivalent
   barrier. In `Enforce`, `AlreadyExists` is a collision and is never signed or
   adopted. `Audit` retains the explicitly insecure legacy fallback described
   below and reports such an unsigned collision.
2. The create response supplies the worker UID and resourceVersion. The manager
   builds the claims and patches the claims, key ID, and proof together using
   JSON Patch tests for both UID and resourceVersion.
3. The manager re-reads and verifies the object, then removes the proof-pending
   barrier with the authenticated UID/resourceVersion write boundary. For the
   remote Workload this is the CAS transition to `spec.active=true`.
4. A proof-patch or verification failure triggers UID-preconditioned
   compensation. Redispatch stays blocked until cleanup is confirmed.

For a multi-Workload adapter, the manager first creates, proves, and verifies
the non-runnable shared controller. Only then can it place that exact remote UID
in each proof-pending `component-workload` claim. It proves and activates the
current component Workloads before calling the adapter to remove the shared
controller barrier. Existing authenticated shared controllers can authorize
later component creation. A failure or restart at any boundary resumes from
verification and never substitutes a same-name controller.

A crash or ambiguous API result between Create and verified proof installation
leaves an unsigned object. Reconciliation does not sign, adopt, or automatically
delete it because it cannot distinguish its own committed create from a
collision. It emits an Event and blocks redispatch pending operator review. The
proof-pending barrier prevents honest partial failure from executing; an
authorized worker mutator can remove that barrier, which is denial of service
within the stated non-goals.

The proof is not spec-bound, so ordinary manager-authorized spec writes do not
need a second proof write. If elastic slicing or another supported transition
changes the authenticated Workload association, the manager patches public
association metadata, new claims, and the new proof atomically under the
authenticated UID and resourceVersion, then re-reads and verifies.

While manager state exists, reconciliation compares claims with current manager
authorization. When that state is absent, garbage collection may use a valid
envelope only to delete the exact historically manager-created worker UID; it
may not relay status or authorize an operational write. The sole write exception
is the provenance-annotation-only key reissuance protocol below. Missing keys,
unknown claim versions, invalid claims, or an unreachable worker preserve the object.
Shared controllers additionally require the group-lifecycle check above.

### Configuration, rollout, and rotation

The alpha feature gate is `MultiKueueAuthenticatedRemoteIdentity`, default
false. Non-nil provenance configuration is rejected while the gate is off.
Both configuration APIs add the same optional field to `MultiKueue`, with
conversion preserving every value and no mode defaulting:

```go
type RemoteObjectIdentityMode string

const (
    // RemoteObjectIdentityAudit signs new objects and reports truly unsigned
    // legacy objects, but does not provide enforcement guarantees for them.
    RemoteObjectIdentityAudit RemoteObjectIdentityMode = "Audit"
    // RemoteObjectIdentityEnforce requires valid provenance for every operation
    // on an extant remote object outside the manager-owned bootstrap exceptions.
    RemoteObjectIdentityEnforce RemoteObjectIdentityMode = "Enforce"
)

type MultiKueue struct {
    // Existing fields omitted.
    // RemoteObjectIdentity configures authenticated remote-object provenance.
    // Nil disables provenance even when the feature gate is enabled.
    RemoteObjectIdentity *MultiKueueRemoteObjectIdentity `json:"remoteObjectIdentity,omitempty"`
}

type MultiKueueRemoteObjectIdentity struct {
    // Mode selects authenticated remote-object provenance behavior. It is
    // required and has no default. Possible values are:
    // - "Audit": sign new objects, report truly unsigned legacy objects, and
    //   permit those objects to use prerequisite public-identity behavior;
    // - "Enforce": require valid provenance for every operation on an extant
    //   remote object outside the manager-owned bootstrap exceptions.
    Mode RemoteObjectIdentityMode `json:"mode"`
    // SigningKeysSecretName names a Secret in the controller Namespace. Each
    // data key is a derived key ID and each value is at least 32 key bytes.
    SigningKeysSecretName string `json:"signingKeysSecretName"`
    // RegistryConfigMapName names the explicitly initialized identity-type
    // immutable registry snapshot in the controller Namespace.
    RegistryConfigMapName string `json:"registryConfigMapName"`
    // RegistryDigest is the exact lowercase hexadecimal SHA-256 digest of the
    // configured registry snapshot. It must match the ConfigMap name suffix
    // and the digest recomputed from the canonical payload.
    RegistryDigest string `json:"registryDigest"`
    // ActiveKeyID selects the key used for new or reissued proofs.
    ActiveKeyID string `json:"activeKeyID"`
    // AcceptedKeyIDs selects at most three verification keys. Order has no
    // meaning, duplicates are invalid, and the set must include activeKeyID.
    AcceptedKeyIDs []string `json:"acceptedKeyIDs"`
}
```

`Audit` signs new objects and reports unsigned legacy objects, but permits a
truly unsigned object to follow the prerequisite public-identity behavior. It
provides migration telemetry, not the enforcement security properties. A
partial proof, malformed claims, unknown key/version, invalid MAC, or a signed
claim mismatch always fails closed and never receives audit fallback. `Enforce`
requires a valid proof for every operation on an extant object except the
manager-owned proof-pending Create, UID/resourceVersion-guarded initial-proof
Patch, verification read, and UID-preconditioned compensation defined above.
Those bootstrap capabilities are internal and are never exposed through the
adapter client. There is no legacy-acceptance gate inside `Enforce`. Because
`Audit` deliberately keeps no trusted per-object
legacy allowlist, it cannot distinguish a genuinely legacy object from an
object whose worker principal removed all three provenance annotations. Such an
object receives insecure audit fallback and is reported as unsigned. Presence
of any one provenance annotation invokes strict parsing with no fallback.
Operators needing the stated security properties must use `Enforce`.

Validation requires an explicit mode, Secret name, initialized immutable
registry ConfigMap name, and exact 64-character lowercase hexadecimal registry
digest in the controller Namespace, an active key present in the accepted set,
at most three distinct accepted IDs, and at least 32 key bytes. A key ID is not
operator-chosen: it is
the unpadded base64url encoding of the first 16 bytes of SHA-256 over the key
bytes. The Secret data key must equal that derived ID. Changing bytes under an
existing ID is therefore rejected consistently across restart and split-replica
reload, absent a computational collision in the truncated digest. Reintroducing
the exact same retired key and ID is not prevented by the key-ID derivation and
is a privileged keyring rollback outside this proposal's worker threat model.
Random key generation remains an operator responsibility; byte length is not
claimed to prove entropy. Missing or invalid key or registry configuration makes
affected AdmissionChecks inactive and prevents
creation, status relay, writes, and automatic cleanup.

In both v1beta1 and v1beta2, nil means disabled, `Mode` has no default, unknown
enum values and duplicate IDs are rejected, and `AcceptedKeyIDs` is compared as
an unordered set. Conversion round trips nil and every explicit value without
enabling the feature.

The rollout sequence is:

1. merge and deploy the #14753/#14761 prerequisite hardening;
2. upgrade every manager replica to a proof-capable binary with the feature
   gate still off and provenance configuration nil;
3. with provenance configuration still nil, initialize and back up the
   immutable identity-type registry snapshot, but do not yet install the
   `RemoteObjectIdentity` configuration;
4. pause MultiKueue dispatch, enable the feature gate on every replica while
   configuration remains nil, and make every gate-off replica ineligible for
   leadership;
5. while dispatch remains paused, install the complete `Audit` configuration on
   every replica, including the keyring and exact registry name-and-digest pin;
   make every nil-configuration replica ineligible for leadership, then resume
   dispatch;
6. inventory and drain or recreate every unsigned legacy object; audit mode
   never retroactively signs it;
7. switch every replica to `Enforce` only after a complete reachable-worker
   inventory reports no unsigned objects and the leader repeats that check
   under the transition barrier; and
8. never return an enforcing deployment to an older or audit-only leader while
   signed objects remain.

Enforce entry, accepted-key removal, and every identity-type-registry pin
advance are security state transitions. On leader acquisition or a proposed transition,
the controller marks affected AdmissionChecks inactive, stops new remote
creates and association changes, and runs a fresh inventory using the effective
old and successor registries and configuration. For registry transitions it
also requires the successor's exact predecessor, generation, immutable payload,
name, and digest pin. It commits the transition only if
the predicate holds, then resumes dispatch. A failure or leadership change
retains the last valid mode, keyring, and registry pin and requires the new
leader to repeat the inventory. Deployment ordering must ensure that no binary
or configuration with the earlier security state remains eligible for
leadership. The
`kueuectl` result is operator preflight evidence; it never replaces this
controller-side barrier.

Rotation uses verify-and-reissue reconciliation. Operators first add the new
derived ID and key bytes, roll every replica with old-active plus new-accepted,
then roll every replica with the new key active. No replica may sign with the
new key until every replica accepts it. Every object in all five roles is then
verified with its old key and CAS-reissued with identical claims except for the
signed active key ID. The single patch tests the worker UID, resourceVersion,
old external key ID, old claims bytes, and old proof; it changes only the three
provenance annotations. A verification reread is required before inventory
counts the object as rotated.

This constrained patch is the only managerless write: the self-contained old
proof authenticates the exact UID, and no association or operational field can
change. Missing, partial, malformed, unknown-key, or invalid proofs never receive
this exception. A shared controller is reissued when its proof is valid and
either its exact live manager-controller UID still authorizes it or manager
state is gone but at least one authenticated component Workload references it.
If no component remains and the manager controller is absent or awaiting
deletion, it is garbage-collected instead. Any unsigned, invalid, or
unverifiable possible component blocks both reissuance and key retirement; it
is never treated as absent. An inventory must enumerate every configured worker
and supported object role; an unreachable worker blocks key retirement. Old keys
remain verification-only until convergence or a full drain is proven. If three
retained keys are already required, another rotation is rejected. Metrics use
bounded outcomes and key roles (`active` or `retiring`), not operator-chosen key
IDs; the audit command provides per-key details.

The operational surface is `kueuectl multikueue identity audit`, backed by the
same inventory library used by Secret reload and mode-transition validation. It
uses manager configuration and existing worker credentials; enumerates the
union of active and persisted item/list GVK and role descriptors; lists the
declared GVKs without relying only on forgeable label selectors; and follows
every Kubernetes list continuation token. Machine-readable output records the
effective configuration digest, persisted-registry generation and digest,
active-adapter-registry digest, accepted and active derived key IDs, generation
time, and, for every worker, the `MultiKueueCluster` name/UID, reachability,
completeness, per-role/key/outcome counts, and errors. It never includes key
bytes, claims, or proofs. Error fields contain bounded error codes plus the
worker, operation, and declared GVK context; untrusted API error text and object
metadata pass through the same redactor used by Events and logs and are not
embedded in machine output.

Exit status 0 requires every configured worker UID to be reachable, every role
to be completely enumerated with no unknown adapter, list, decode, or
verification error, and the requested predicate to hold (`no unsigned` before
Enforce or `no retiring key` before removal). Exit status 2 reports a complete
inventory whose predicate is false; status 1 reports an incomplete or
operationally failed inventory. The output is evidence, not a reusable approval
token: Enforce startup and an accepted-key removal perform a fresh inventory
bound to their current cluster UIDs, effective config, keyring IDs, and adapter
registry. Descriptor removal additionally requires zero possible objects of
that descriptor on every worker while dispatch is paused. Premature key or
descriptor removal is rejected, the last valid keyring and registry remain in
memory, and affected AdmissionChecks report the condition instead of silently
losing verification. Stale replicas cannot emit an old-key proof after the
documented active-key rollout barrier.

### Test Plan

[x] I/we understand the owners of the involved components may require updates to
existing tests to make this code solid enough prior to committing the changes
necessary to implement this enhancement.

#### Unit tests

Authenticated provenance adds committed golden vectors for all five valid
claims forms and every key selector. Rejection vectors cover every missing
required field, populated forbidden field, and invalid cross-role form. Fuzz
properties cover deterministic encoding, decode/encode identity,
field-boundary separation, noncanonical and oversized input rejection, and
one-field mutation. Table tests cover partial annotations, malformed base64url,
unknown version/key, wrong cluster/object/UID/association/role, cross-role
replay, and constant-time MAC verification.

Configuration tables cover gate-off rejection, `Audit`, `Enforce`, both config
API versions and conversion, every invalid keyring, Secret reload, active and
verification-only keys, the three-key ceiling, and an in-use-key removal and
restore. They also cover same-ID byte mutation, derived-ID mismatch, split-replica
reload, nil opt-out, enum rejection, unordered accepted IDs, and Alpha/Beta/GA
gate defaults. Registry initialization tests cover first Create, identical
idempotent replay, different preexisting bytes, attempted initialization after
configuration, exact latest-backup restoration, fatal post-initialization
deletion, and rejection of a valid older backup after an append or removal and
controller restart. They cover immutable content-addressed naming, configured
name/digest mismatch, generation skips, wrong predecessor links, concurrent
different successors, backup-write failure without pin advancement,
initial-stale versus latest restoration, and unsupported configuration-pin
rollback.
Adapter conformance tables cover all five roles, proof-pending
barriers, shared-controller group authorization, declared item/list GVKs,
illegal role/plan-field combinations, an undeclared GVK, stale cached manager
objects, same-name manager replacements, and the rule that signed-invalid
objects never fall back to audit behavior. A common authenticated-client table
exercises every exposed post-bootstrap route: typed and unstructured `Get` and
`List`; rejected `Create`; `Update`, `Patch`, and `Delete`; fail-closed
`DeleteAllOf`; `Status().Update` and `Status().Patch`; and every exposed
`SubResource(...).Get/Create/Update/Patch` operation. Each has a valid-proof
control and missing, invalid, wrong-role, wrong-UID, and same-name-replacement
negatives. Mixed valid/invalid lists fail the whole read rather than filtering a
possible object into apparent absence. Registry tests cover immutable-snapshot
Create collisions, missing/corrupt state, retained custom GVK enumeration
without a live adapter, descriptor-removal races, and transition-barrier
restart and leader handoff.
Legacy-adapter tables verify unchanged nil-config behavior, audit reporting with
public-identity guards, complete-removal audit fallback, and pre-dispatch
AdmissionCheck inactivity in Enforce.

Golden JSON and YAML audit-output fixtures cover a complete successful result,
a complete predicate-false result, and an operationally incomplete result.
They assert the stable presence of configuration and registry digests, registry
generation, worker names and UIDs, reachability, completeness, bounded
per-role/key/outcome counts, and errors. Redaction tables inject malformed
annotations and errors containing key-, claim-, and proof-like material and
assert that key bytes, raw claims, and proof tags never appear in command
output, Events, logs, or returned error strings. Metric descriptor tests permit
only bounded role, outcome, and active-or-retiring labels and reject raw or
derived key IDs and other operator-controlled label values.

#### Integration tests
Integration tests will use mocked worker-cluster clients that provide predefined
responses and exercise error scenarios,
including situations like:

* Job is created across multiple clusters and admitted in one.
* Job is admitted at the same time by two clusters.
* Job is rejected by a cluster.
* Worker cluster doesn't have the corresponding namespace.
* Worker cluster doesn't have the corresponding local/cluster queue.
* Worker cluster is unresponsive.
* Worker cluster deletes the job.
* Job is correctly finished.
* Job finishes with an error.
* Job status changes frequently.
* A worker pre-creates an object with copied public MultiKueue metadata.
* For each of the five Workload, component-Workload, controller,
  PodGroup-member, and shared-controller roles, a
  worker replaces the object before Create, proof Patch, verification Get,
  association Patch, status relay, cleanup Get, and final Delete. The
  replacement is preserved and its status is not relayed.
* Real API-server tests exercise JSON Patch UID and resourceVersion tests and
  UID-preconditioned deletion. Interceptor tests add server-side success followed
  by client timeout for Create, proof Patch, association Patch, and Delete.
* Real manager-API tests exercise initial immutable registry ConfigMap Create,
  concurrent first-install attempts, exact idempotent replay, different
  preexisting bytes, and competing successor snapshots. They delete an enabled
  snapshot, observe AdmissionCheck inactivity across controller restart, reject
  an initial but stale backup after descriptor append and adapter removal, and
  restore only the latest exact content-addressed backup through API Create with
  a changed ConfigMap UID. A parallel recovery path proves active adapters are
  never used to reconstruct missing state. Append and removal cover successor
  generation, predecessor, backup-before-pin sequencing, config name/digest
  pin, and old/new-replica transition-barrier conflicts.
* The manager restarts after Create, after proof Patch success but before its
  response, after verification but before barrier removal, and during
  UID-preconditioned compensation. Redispatch remains blocked until the exact
  object is verified or cleanup is confirmed.
* Proof-pending Jobs, Pods, and custom objects remain non-runnable through every
  honest partial-failure state. An adapter without a barrier is rejected from
  enforcement mode.
* Shared-controller bootstrap exercises replacement and restart before and
  after controller proof, component-Workload proof, component activation, and
  shared-barrier removal. No component claim accepts a controller UID obtained
  before proof verification.
* A proof-pending remote Workload remains `active=false` and cannot reserve
  quota, trigger preemption, or become admitted after Create, proof-Patch
  timeout, manager restart, or verification failure. Only successful proof
  verification permits its UID/resourceVersion-guarded activation.
* Association rollback on the same worker UID is rejected while live manager
  state expects the newer association. Status and other deliberately unsigned
  fields can change without invalidating provenance. Same-UID spec-history
  integrity is explicitly not asserted.
* Managerless garbage collection deletes a valid exact-UID Workload or
  single-Workload owner, preserves missing/partial/invalid/unknown-key proofs,
  and preserves a shared controller while any authenticated component Workload
  remains.
* Rotation covers old-active, old-active plus new-accepted, new-active plus
  old-verification-only, reissue of every role, unreachable-worker retirement
  refusal, three-key exhaustion, premature removal, and restoration. One and
  all replicas restart at every stage.
* For every managerless role, old-to-new reissuance tests successful CAS,
  invalid or unavailable old keys, replacement before the patch, patch success
  followed by client timeout, restart on both sides of the patch, unchanged
  public association metadata, and the shared-controller component-lifecycle
  check. A valid shared controller with authenticated live components is
  reissued; one with no components is garbage-collected; an invalid or
  unverifiable possible component blocks reissue, deletion, and key retirement.
  A live exact manager controller with zero current components also authorizes
  reissue during initial creation or scale-to-zero.
* Upgrade tests cover gate off, `Audit`, and `Enforce`; truly unsigned legacy
  objects and fully stripped objects receive reported audit-only compatibility,
  whereas one-annotation-only, malformed, unknown-key, invalid-MAC,
  stripped-in-enforcement, and valid signed objects never downgrade in
  `Enforce`. The rollout test follows the exact phase sequence above: gate-off
  plus non-nil configuration is rejected; registry initialization leaves
  configuration nil; dispatch remains paused across the gate-on/config-nil and
  mixed Audit-configuration rolls; no gate-off or nil-configuration replica is
  leadership-eligible at the next phase; and no unsigned remote object is
  created through either mixed-replica boundary. No pre-proof binary is leader
  after audit is enabled.
* Inventory tests cover multi-page lists for every GVK, objects created and
  deleted during inventory, worker disconnect and reconnect, changed
  `MultiKueueCluster` UID, unknown or failing adapters, config/registry digest
  changes, retained descriptors after adapter removal, all three exit statuses,
  fresh reruns at Enforce and key or descriptor removal, transition-lock races,
  premature-removal rejection, and preservation of the last valid keyring and
  registry. Command integration tests compare the golden JSON/YAML schemas,
  exercise all three exit statuses, and verify output, Events, logs, errors, and
  metric labels do not disclose key bytes, raw claims, or proof tags even when
  worker objects and API errors contain malicious material. Only the audit
  command's schema exposes validated configured derived key IDs; Events, logs,
  errors, and metric labels do not.

#### E2E tests
Should be created and cover similar use cases as integration tests. The
authenticated-provenance cases below extend the original JobSet-focused plan.

Authenticated-provenance E2E coverage uses three clusters and a namespaced
worker actor matching the threat-model RBAC. It validates legitimate dispatch,
status, and cleanup for all built-in roles; precreation and replacement of every
PodGroup member; shared-controller lifetime; proof-pending restart recovery;
the complete audit-to-enforce transition; and active-key rotation with an
unreachable worker blocking retirement. It also verifies that no old or
audit-only leader participates after enforcement begins.

### Graduation Criteria

Alpha defaults the gate off and requires the complete claims codec and test
vectors, all built-in adapter roles and proof-pending barriers, `Audit` and
`Enforce` validation in v1beta1 and v1beta2, key reload/rotation, and fail-closed
lifecycle tests. Beta requires two
release cycles of successful audit-to-enforce upgrades, multi-replica rotation,
custom-adapter conformance coverage, and no unresolved provenance bypasses; the
gate defaults on at Beta, but nil configuration remains an explicit opt-out and
neither `Audit` nor `Enforce` is defaulted. At GA the gate is locked true and
removed no earlier than two releases later. GA makes the capability available
by default, not enforcement: nil configuration continues to disable provenance
until a future design provides safe key bootstrapping. Stable also requires
documented key disaster recovery, scale evidence for PodGroups and inventory,
and a supported decoder and key-retirement policy across skew windows.

## Implementation History

- 2026-08-23: Proposed authenticated remote-object provenance.

## Drawbacks

- Operators must provision, protect, rotate, and recover a manager-side keyring.
- Proof bootstrap adds worker API writes and can require manual recovery after
  an ambiguous Create.
- Complete enforcement requires adapter conformance and full worker inventory.
- The design authenticates provenance, not arbitrary worker execution history.

## Alternatives

* **Public metadata or random capabilities** cannot authenticate replacements
  because a worker reader can copy them.
* **Signing complete specs** would add same-UID integrity but requires a stable
  per-adapter canonical projection across defaulting, conversion, admission,
  and arbitrary unstructured CRDs. It is deliberately a separate design.
* **Persisting every worker UID in manager state** avoids keys but requires an
  unbounded PodGroup identity ledger and durable tombstones for managerless GC.
* **A worker validating webhook** can make creation atomic but adds a highly
  available worker admission dependency and distributes trust configuration.
* **Deriving keys from kubeconfig credentials** couples unrelated rotations and
  exposes bearer material to cryptographic code paths. Signing keys stay
  separate.
