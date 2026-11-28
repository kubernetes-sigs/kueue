# Maintainers review: rules

The full rules behind the [checklist](SKILL.md#checklist). Each rule says what to do and
why, lists the reviewers whose profile has it, and links one or two of their comments as
precedents. The links point to the exact comment, and the quotes are copied from it.
More precedents, and each reviewer's process and focus, are in `reviewers/<login>.md`.

These rules add to the
[Coding Guidelines](../../../../site/content/en/community/contribution_guidelines/coding_guidelines.md),
the [Logging Policy](../../../../site/content/en/community/contribution_guidelines/logging_policy.md)
and [Writing tests](../../../../site/content/en/community/contribution_guidelines/writing_tests.md).
Where they disagree, the guidelines win.

## A. Diff and scope

### MR-A1 [simple] Change only the lines the fix needs

Keep formatting, blank lines and names as they are unless the change needs them. A
small diff is faster to review and cherry-picks to release branches with fewer conflicts.
Move code unchanged, in its own PR.

Raised by: mimowo, mbobrovskyi, tenzen-y, sohankunkerkar, ahg-g, gabesaba, kerthcet.

- mimowo on [#4086](https://github.com/kubernetes-sigs/kueue/pull/4086#discussion_r1939288583): "nit, revert empty line changes since this is to be cherry-picked, so to minimize changes of unnecessary conflicts."
- mbobrovskyi on [#11002](https://github.com/kubernetes-sigs/kueue/pull/11002#discussion_r3206550951): "Please revert the unnecessary removal of empty lines."

### MR-A2 One concern per PR

Send side fixes, refactors and "while I was here" changes as separate PRs. Land
mechanical prerequisites (new helpers, renames, moves) first, in a small preparatory PR
with no behaviour change. Tide squash-merges PRs, so split work into PRs, not commits. A
focused bug fix can be cherry-picked on its own.

Raised by: all eight reviewers.

- mimowo on [#16098](https://github.com/kubernetes-sigs/kueue/pull/16098#discussion_r4093436872): "This looks like a good fix / performance optimization unrelated to the PR, could you send a bugfix for that?"
- mbobrovskyi on [#18880](https://github.com/kubernetes-sigs/kueue/pull/18880#issuecomment-6920805612): "Could we split this into two PRs? PRs are squashed on merge, so the commit split is lost."

### MR-A3 [simple] Add code with its first user, and keep the exported surface small

Add API fields, constants, options and code paths in the PR that uses them, and delete
the ones your change leaves without a caller. Unexport what only its own package uses.
External integrations build against `jobframework`, so change its exported functions
only when the change needs it.

Raised by: all eight reviewers.

- mimowo on [#12378](https://github.com/kubernetes-sigs/kueue/pull/12378#discussion_r3450676234): "This looks unused for now. Let's drop the unused ones out, so that we don't forget to add tests for them once they become used."
- mbobrovskyi on [#18411](https://github.com/kubernetes-sigs/kueue/pull/18411#discussion_r4491181649): "Since this is now called only from cmd/kueue/main.go, I'd drop the exported wrapper"

### MR-A4 Agree on the design before the code

Take API changes and new scheduling mechanisms through a KEP (or a KEP update) first,
then implement what the KEP says. Update the KEP in the same release when the
implementation departs from it.

Raised by: mimowo, tenzen-y, sohankunkerkar, alculquicondor.

- mimowo on [#10587](https://github.com/kubernetes-sigs/kueue/pull/10587#discussion_r3102044825): "The API changes should first go via KEP design phase."
- tenzen-y on [#6541](https://github.com/kubernetes-sigs/kueue/pull/6541#pullrequestreview-3119290372): "This seems to be a new scheduling mechanism. So, could you open KEP"

## B. Simplicity and reuse

### MR-B1 [simple] Reuse before you write

Search `pkg/util`, the test wrappers, `test/util/behavioral` and the sibling
integrations for a helper, constant or pattern, and call it. Keep one implementation of
each piece of logic: duplicates drift apart.

Raised by: all eight reviewers.

- mimowo on [#9726](https://github.com/kubernetes-sigs/kueue/pull/9726#discussion_r2906914378): "Instead of that use our helper in pkg/util/client/client.go"
- sohankunkerkar on [#18632](https://github.com/kubernetes-sigs/kueue/pull/18632#discussion_r4571411927): "nit: Could we share these name checks with `validateDeviceClassMappings` in a small helper, so the two lists can't drift?"

### MR-B2 [simple] Every guard, variable and parameter earns its place

Delete guards that cannot fire, single-use variables and wrappers, parameters that
always get the same value, and empty implementations of optional interfaces. Rely on
Kueue's own API validation for its invariants; read external APIs (KubeRay, TrainJob)
defensively. Derive values from existing state rather than storing a second copy. Pass a
new optional dependency as a functional option (`...Option`).

Raised by: all eight reviewers.

- mimowo on [#14316](https://github.com/kubernetes-sigs/kueue/pull/14316#discussion_r4085483924): "no need o implement an optimal interface with empty implementation."
- mimowo on [#3848](https://github.com/kubernetes-sigs/kueue/pull/3848#discussion_r1886337797): "It duplicates the information which is already present. It is hard to assess correctness with the duplicated information."

### MR-B3 [simple] Keep control flow flat and named

Use guard clauses and early `return`/`continue`, and test the positive case first. Move
compound conditions and non-trivial blocks (loop bodies, lock sections) into small
named functions in the same file, in place of anonymous functions (local closures)
that hold logic. In tests, chain wrapper methods or `Clone()` in place of closures.

Raised by: mimowo, tenzen-y, sohankunkerkar, gabesaba, alculquicondor.

- mimowo on [#18397](https://github.com/kubernetes-sigs/kueue/pull/18397#discussion_r4490374279): "Can you wrap this in a named function to reflect better on the intention of the code?"
- gabesaba on [#5177](https://github.com/kubernetes-sigs/kueue/pull/5177#discussion_r2079657152): "prefer guard clause to reduce nesting/improve readability"
- tenzen-y on [#15885](https://github.com/kubernetes-sigs/kueue/pull/15885#discussion_r4174829380): "Instead of an anonymous function, we should define a dedicated function in this file."

### MR-B4 [simple] Fix every similar place, siblings included

When you change one integration, controller or test, apply the same change to its
siblings (the other Job integrations, MultiKueue adapters, controllers), or say in the
PR why a sibling differs. When a reviewer flags one line, fix every occurrence of the
pattern in the PR before you push.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, alculquicondor, gabesaba, ahg-g.

- alculquicondor on [#2026](https://github.com/kubernetes-sigs/kueue/pull/2026#discussion_r1578316269): "Apply the same recommendation for all the other job types."
- tenzen-y on [#5215](https://github.com/kubernetes-sigs/kueue/pull/5215#discussion_r2084731063): "I have the same comments in other places."

### MR-B5 [simple] Named constants and typed keys

Replace magic numbers and string literals with named constants, existing API constants
(`corev1.ResourceCPU`) or the object's own field (`lq.Name`). Key workload state by
`workload.Reference`, use `sets.Set[T]` for membership, and give other keys and reasons
a dedicated type.

Raised by: mimowo, tenzen-y, mbobrovskyi, gabesaba, kerthcet.

- mimowo on [#17680](https://github.com/kubernetes-sigs/kueue/pull/17680#discussion_r4413978981): "the benefit of using workload.Reference is strong typing, plus it immediately informs the reader workload is the key."
- tenzen-y on [#11943](https://github.com/kubernetes-sigs/kueue/pull/11943#discussion_r3364001251): "Can you make the delay time a constant at the top of this file?"

### MR-B6 Put code where it belongs

Put feature-specific code in its own file (`tas_spreading.go`) or type with its own
lock, in place of new loose fields on big structs. Keep generic code generic: put
integration-specific behaviour behind an optional `JobWith…` interface or in the
integration's package.

Raised by: mimowo, sohankunkerkar, gabesaba, alculquicondor, kerthcet.

- mimowo on [#14820](https://github.com/kubernetes-sigs/kueue/pull/14820#discussion_r4008132846): "Please move the helpers t `tas_spreading.go` all the helper functions, by analogy to tas_elastic_worklaods.go and tas_balanced_placement.go"
- mimowo on [#15677](https://github.com/kubernetes-sigs/kueue/pull/15677#discussion_r4079828497): "We should avoid Ray-specific "if" in the generic reconciler. I think we may introduce an optional interface like JobWithCustomEquivalenceOptions, …"

### MR-B7 Start simple, and back performance claims with numbers

Choose the simpler code until a profile or perf test shows a bottleneck. In the
scheduler, caches and reconcilers, keep per-workload work cheap: no full scans or
allocations per call. Put before/after numbers in the PR for performance work.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, gabesaba, alculquicondor, ahg-g, kerthcet.

- mimowo on [#11210](https://github.com/kubernetes-sigs/kueue/pull/11210#discussion_r3315468352): "I would start simple, and complicate code when we prove this is a bottlneck."
- sohankunkerkar on [#12520](https://github.com/kubernetes-sigs/kueue/pull/12520#discussion_r3494856808): "is O(active + inadmissible) with a heap.List() allocation on every call."

## C. Naming and Go style

### MR-C1 [simple] Names say exactly what the thing is or does

A name claims neither a narrower nor a wider scope than the code has. Rename a function
when your change alters what it does. Pick a specific word over `info`, `result` or
`common`.

Raised by: all eight reviewers.

- mimowo on [#14316](https://github.com/kubernetes-sigs/kueue/pull/14316#discussion_r4099429245): "I think the name of the function is misleading, because it will return true also for regular ElasticJobs"
- sohankunkerkar on [#17526](https://github.com/kubernetes-sigs/kueue/pull/17526#discussion_r4408227953): "This now writes the spec and removes the annotation, so the name is misleading. Maybe `SyncWaitForPodsReady`?"

### MR-C2 [simple] Follow the local naming conventions

Name getters after their field, without `Get`, and use `find` or `resolve` for functions
that compute or search. Write wrapper setters without `Set`. Leave the type and the
surrounding context out of a name (`wls`, not `workloadList`). Give exported and
long-lived names full words. Match the names of sibling functions.

Raised by: mimowo, tenzen-y, sohankunkerkar, alculquicondor, gabesaba, ahg-g, kerthcet.

- mimowo on [#10917](https://github.com/kubernetes-sigs/kueue/pull/10917#discussion_r3181439093): "nit, let's use "find" prefixes, get suggests a simple get operation as if the value was preconputed."
- tenzen-y on [#15000](https://github.com/kubernetes-sigs/kueue/pull/15000#discussion_r4077609453): "NITS: Go uses field names as getter functions."

### MR-C3 Use Kueue's vocabulary

Reuse the term Kueue already has for a concept (admitted and inadmissible for
workloads, `AdmissionCheck`, `reclaim` for borrowed quota), one term per concept. Write
Kueue kinds in PascalCase (`ClusterQueue`, `Workload`), and spell names out in
user-facing text (`MultiKueue`, not MK).

Raised by: tenzen-y, sohankunkerkar, gabesaba, alculquicondor, ahg-g, kerthcet.

- tenzen-y on [#11720](https://github.com/kubernetes-sigs/kueue/pull/11720#discussion_r3404494109): "The API should be descriptive. The `Checks` is a common word. So, we should use Kueue Kueue-specific `AdmissionCheck` terminology here."
- ahg-g on [#135](https://github.com/kubernetes-sigs/kueue/pull/135#discussion_r832063357): "We sort of adopted the terms admitted/inaddmissible rather than scheduled/unschedulable."

### MR-C4 [simple] Use the Go and Kubernetes idioms the codebase uses

Treat nil and empty slices the same and test them with `len()`. Read optional pointers
with `ptr.Deref(p, default)`, default with `cmp.Or`, and compare with
`Quantity.Equal`. Reuse the import alias the codebase already has; add one only on a
collision, and then alias the non-stdlib package.

Raised by: mimowo, tenzen-y, mbobrovskyi, alculquicondor, gabesaba.

- tenzen-y on [#13224](https://github.com/kubernetes-sigs/kueue/pull/13224#discussion_r3892484276): "nit: Using cmp.Or capability."
- mbobrovskyi on [#9781](https://github.com/kubernetes-sigs/kueue/pull/9781#discussion_r2912901559): "We are trying to avoid std aliases."

## D. Comments and docs

### MR-D1 [simple] Comments explain why

Add a comment where a reader would ask why: a deliberate skip, a kept-on-purpose check,
a non-obvious order. Delete comments that restate the code, and keep fix history in the
commit message.

Raised by: all eight reviewers.

- mimowo on [#9615](https://github.com/kubernetes-sigs/kueue/pull/9615#discussion_r2929823945): "nit, but this is self explanatory. I think comments are useful if they bring some readabilty, here the comment just eats one line"
- tenzen-y on [#13571](https://github.com/kubernetes-sigs/kueue/pull/13571#discussion_r3792140261): "It would be better to avoid leaving comments for HOW."

### MR-D2 [simple] Update what the change made stale

Fix every comment, godoc, `ginkgo.By` text and user doc that your change makes false.
Carry the "why" comments over when you move or rewrite code.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, alculquicondor, gabesaba, kerthcet.

- mbobrovskyi on [#17575](https://github.com/kubernetes-sigs/kueue/pull/17575#discussion_r4527225797): "With this change, two comments that explain the default LocalQueue guard are no longer accurate:"
- sohankunkerkar on [#15503](https://github.com/kubernetes-sigs/kueue/pull/15503#discussion_r4031633019): "Comment says "only set when strictly positive" but we set it whenever the pointer is non-nil."

### MR-D3 [simple] Temporary code names its exit

Mark workarounds, compatibility code and fallbacks with a to-do comment that links an
open tracking issue (`(#N): <what to do>`) and says what must happen before the code
can go: a release, a gate graduation, a minimum Kubernetes version.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, alculquicondor, gabesaba, ahg-g.

- sohankunkerkar on [#17526](https://github.com/kubernetes-sigs/kueue/pull/17526#discussion_r4408219986): "When do we stop reading the Workload annotation? I'd add that to the KEP so this fallback has a removal plan."
- alculquicondor on [#245](https://github.com/kubernetes-sigs/kueue/pull/245#discussion_r866852357): "You have to explain what is there \"to do\"."

### MR-D4 Document new API and exported symbols for their reader

Give every new field, type, enum and exported function a doc comment with its semantics,
default and special values (0, unset). List enum values as "Possible values are:". API
godoc is user-facing: use YAML field names, describe behaviour, and name the feature gate
of a gated field or annotation.

Raised by: mimowo, tenzen-y, sohankunkerkar, alculquicondor, gabesaba, ahg-g, kerthcet.

- mimowo on [#10610](https://github.com/kubernetes-sigs/kueue/pull/10610#discussion_r3153211083): "List the possible values, say "Possible values are: " and describe their semantics briefly."
- tenzen-y on [#17559](https://github.com/kubernetes-sigs/kueue/pull/17559#discussion_r4492181368): "API documentation is user-facing. So, we should use non-implementation-specific terms."

### MR-D5 [simple] Ship user docs with the behaviour

When a PR adds a user-visible feature, gate, annotation or integration, update the site
docs in the same PR: extend the page that covers the topic, add labels and annotations
to `site/data/labels_and_annotations.yaml`, and link the canonical page (feature-gate
setup) in place of repeating it. Keep samples in `site/static/examples/` and include them.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, alculquicondor, ahg-g, kerthcet.

- mbobrovskyi on [#17575](https://github.com/kubernetes-sigs/kueue/pull/17575#issuecomment-6772351377): "Could we also add a short note to `site/content/en/docs/tasks/run/deployment.md`?"
- tenzen-y on [#13308](https://github.com/kubernetes-sigs/kueue/pull/13308#discussion_r4105052844): "Instead of a dedicated page, can you just add the description to"

### MR-D6 [simple] zh-CN: mirror mechanical fixes, leave new prose to its owners

Write new or changed prose in `site/content/en` only; the zh-CN owners translate it in
a follow-up. Mirror mechanical fixes (commands, links, tables, typos, what
`make i18n-verify` needs) into the zh-CN counterpart.

Raised by: mimowo, mbobrovskyi.

- mimowo on [#8376](https://github.com/kubernetes-sigs/kueue/pull/8376#discussion_r2667964884): "I would suggest to drop the translation form this PR. Translation can be done in a follow up PR, and we have dedicated reviewers / approvers for that."
- mbobrovskyi on [#5899](https://github.com/kubernetes-sigs/kueue/pull/5899#discussion_r2192528144): "Shouldn't we also update the zh-CN version?"

## E. Logging, errors, events

### MR-E1 [simple] Pick log verbosity from the Logging Policy table

Rare anomalies at V(0)–V(1), main per-object decisions at V(3), decision details at
V(4), traces at V(5). Default logs (V(2)) stay quiet on large clusters. When a branch
"should never happen", log it in place of a comment.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, alculquicondor, gabesaba, ahg-g.

- mimowo on [#17675](https://github.com/kubernetes-sigs/kueue/pull/17675#discussion_r4634625218): "nit, use v3 accorting to our logging policy"
- tenzen-y on [#9795](https://github.com/kubernetes-sigs/kueue/pull/9795#discussion_r2919823066): "the default logs (V2) should be minimized as much as possible."

### MR-E2 [simple] Use the propagated logger and context

Take the logger from `ctrl.LoggerFrom(ctx)`, the one the reconcile propagates, and log
with structured key/values, `klog.KObj` and lowerCamelCase keys, without re-adding keys
it already carries. Pass the caller's `ctx` down (`t.Context()` in tests) in place of
`context.Background()`.

Raised by: mimowo, tenzen-y, mbobrovskyi, alculquicondor, gabesaba, ahg-g.

- tenzen-y on [#6325](https://github.com/kubernetes-sigs/kueue/pull/6325#discussion_r2245709937): "We should use the propagated logger from reconciler since it has reconcile request objects."
- mimowo on [#8964](https://github.com/kubernetes-sigs/kueue/pull/8964#discussion_r2820930836): "Don't use background context, propagate from the top. This is important for logging"

### MR-E3 Handle every error exactly once

Return the error up the stack, or log it and say why it is skipped. Log it once: wrap
with `%w` and context in place of a second log line. Drop the `error` return from a
function that cannot fail.

Raised by: mimowo, sohankunkerkar, mbobrovskyi, alculquicondor, gabesaba, ahg-g.

- mimowo on [#11141](https://github.com/kubernetes-sigs/kueue/pull/11141#discussion_r3267445590): "do not silence errors, at least log them"
- gabesaba on [#8709](https://github.com/kubernetes-sigs/kueue/pull/8709#discussion_r2774172902): "and is it correct to swallow the error here?"

### MR-E4 Panic only on a broken internal contract

When a bad value can come only from a Kueue programming error at an internal call site,
`panic` with a message that names the contract. Log anomalies from cluster state at
V(0)–V(1), and validate user input and return an error.

Raised by: mimowo, alculquicondor, gabesaba.

- mimowo on [#8993](https://github.com/kubernetes-sigs/kueue/pull/8993#discussion_r2770698746): "I would rather panic here with the message. I looked at all the so-far invocations and I think in all cases here should be no nil passed. This will …"
- alculquicondor on [#2027](https://github.com/kubernetes-sigs/kueue/pull/2027#discussion_r1584947024): "Use `panic` for developer errors."

### MR-E5 [simple] Events, conditions and metrics change only on a real change

Emit an Event, update a condition or count a state-change metric only when the status,
reason or message changes, so a repeated reconcile or a restart stays silent. Counters
of attempts (such as `admission_attempts_total`) still count every attempt. Keep user-visible messages
short and actionable, and wrap user-influenced text with `api.TruncateEventMessage` or
`api.TruncateConditionMessage`.

Raised by: mimowo, sohankunkerkar, alculquicondor, ahg-g.

- sohankunkerkar on [#16857](https://github.com/kubernetes-sigs/kueue/pull/16857#discussion_r4339639770): "Could we emit only when the QuotaReserved reason or message changes"
- alculquicondor on [#176](https://github.com/kubernetes-sigs/kueue/pull/176#discussion_r843140349): "I think the rest of the string only make sense as part of the log, not as a user-visible message."

## F. Controllers and API calls

### MR-F1 Send fewer API requests

Patch only when the computed object differs, write all changes to one part of an object
in one request, and decide inside loops but patch once after them. Select objects with a
field index or label selector in place of listing a namespace and filtering in Go. Pass
down an object the caller already fetched.

Raised by: mimowo, sohankunkerkar, mbobrovskyi, alculquicondor, ahg-g.

- mimowo on [#13015](https://github.com/kubernetes-sigs/kueue/pull/13015#discussion_r3577039133): "Seems like we may be producing unnecessary API requests. Shouldn't we guard sending the request by equivalence check?"
- mbobrovskyi on [#9091](https://github.com/kubernetes-sigs/kueue/pull/9091#discussion_r2794184748): "There could be thousands of pods in this namespace. We need to add indexer for this."

### MR-F2 In-memory state is cleaned up and never shared by mutation

Clean caches, trackers and metric series on every delete or finish path, from one
helper, with a test that fails on a leak. Deep-copy before you change an object from the
informer cache or a shared snapshot.

Raised by: mimowo, sohankunkerkar, alculquicondor, gabesaba, kerthcet.

- mimowo on [#18219](https://github.com/kubernetes-sigs/kueue/pull/18219#discussion_r4602944076): "This is in-memory state. Do we have some unit or integration tests to confirm there is no leak?"
- alculquicondor on [#191](https://github.com/kubernetes-sigs/kueue/pull/191#discussion_r846321066): "This is modifying the object from the informer Cache."

### MR-F3 Survive restarts and leader changes

After a restart every object arrives as `Create`, so handle `Create` like an update of
existing state, and rebuild caches from status. Know whether a new controller runs only
on the leader, and test restarts with `fwk.StopManager`/`fwk.StartManager`.

Raised by: mimowo, tenzen-y, alculquicondor.

- mimowo on [#7312](https://github.com/kubernetes-sigs/kueue/pull/7312#discussion_r2546320227): "We need to also add the code to Create, the reason is that this event is called on Kueue restart."
- tenzen-y on [#6601](https://github.com/kubernetes-sigs/kueue/pull/6601#discussion_r2305216917): "Can you support LeaderElection?"

### MR-F4 Event handlers filter; Reconcile does the work and retries

Keep `Create`/`Update`/`Delete` handlers to cheap checks and enqueueing. Do API calls
and heavy work in `Reconcile`, and return transient errors so the workqueue backoff
applies, in place of a fixed `RequeueAfter`.

Raised by: mimowo, alculquicondor, gabesaba.

- mimowo on [#5873](https://github.com/kubernetes-sigs/kueue/pull/5873#discussion_r2345015783): "it is performing heavy operations (API requests) in event handlers, which are supposed to be very fast"
- mimowo on [#11943](https://github.com/kubernetes-sigs/kueue/pull/11943#discussion_r3371526145): "Unconditional constant using generic `draRequeueDelay` delay on errors is usually a code smell, because it eliminates the controller-runtime native …"

### MR-F5 Webhooks stay fast and handle UPDATE

Send no API requests from webhooks or from functions they call (`PodSets()`); put
writes in the reconciler, where they are retried. Check defaulting and validation on
UPDATE as well as CREATE (running objects, objects from before the upgrade, the default
LocalQueue), and test it.

Raised by: mimowo, mbobrovskyi.

- mimowo on [#9726](https://github.com/kubernetes-sigs/kueue/pull/9726#discussion_r2920021916): "we cannot call the Patch from within PodSets(), because PodSets() is called from webhooks for validation and defaulting. Those need to be super fast, …"
- mbobrovskyi on [#17575](https://github.com/kubernetes-sigs/kueue/pull/17575#issuecomment-6522745235): "Default() calls ApplyDefaultLocalQueue on UPDATE as well."

## G. Feature gates, API, upgrades

### MR-G1 Gate new and risky behaviour, and check the gate first

Put new Alpha logic and disruptive fixes (including fixes that will be cherry-picked)
behind a feature gate. Check the gate first, with an explicit `features.Enabled(...)`
where sibling code checks it, so the gate-off path runs today's code unchanged and does
no extra work. Gate the registration of a new controller, index or watch.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, alculquicondor, kerthcet.

- tenzen-y on [#17781](https://github.com/kubernetes-sigs/kueue/pull/17781#discussion_r4645895869): "To clarify, all new alpha logic should be guarded by a feature gate."
- mimowo on [#5861](https://github.com/kubernetes-sigs/kueue/pull/5861#discussion_r2184621304): "nit, First check feature gate to ensure any "new" functions are not called when disabled"

### MR-G2 [simple] Standard gate stages and entries

Alpha is off by default, Beta is on. A bug-fix gate is Beta on main and ships as Alpha,
off, on release branches. In `pkg/features/kube_features.go`, add one versioned line
with the next unreleased minor version, link the KEP or tracking issue, and name the gate
after its real scope with the area prefix (`TAS…`, `MultiKueue…`).

Raised by: mimowo, tenzen-y.

- mimowo on [#15049](https://github.com/kubernetes-sigs/kueue/pull/15049#discussion_r3915662157): "I think this is a bugfix and for bugfixes we rather introduce FGs which are enabled by default. When CPking for risky changes we sometimes change to …"
- tenzen-y on [#13797](https://github.com/kubernetes-sigs/kueue/pull/13797#issuecomment-5355108806): "Those cherry picks should have "0.18, Alpha, disabled by default" and "0.19, Alpha, disabled by default" changes."

### MR-G3 Add the smallest API that a user story needs

A field cannot be removed without a new API version. Reuse an existing field, condition
or lifecycle first, then prefer the lightest option (hard-coded behaviour behind the
gate, an alpha annotation). Extend an existing metric with a label before adding one.

Raised by: mimowo, tenzen-y, sohankunkerkar, alculquicondor, gabesaba, ahg-g, kerthcet.

- tenzen-y on [#5981](https://github.com/kubernetes-sigs/kueue/pull/5981#discussion_r2293140047): "The API has highly maintainance costs since we can NOT break that w/o API version change"
- mimowo on [#14554](https://github.com/kubernetes-sigs/kueue/pull/14554#discussion_r3966756816): "Do we have use cases for this? If the use cases are unclear for now let's drop."

### MR-G4 Follow the Kubernetes API conventions

Object references end in `Ref`, sibling suffixes match (`…State`), state goes into
`status.conditions`, and Kueue-computed values go into `status`. Use an enum `mode` in
place of a bool, mark every field `+optional` or `+required`, and prefix new Go type names
with the parent kind.

Raised by: mimowo, tenzen-y, alculquicondor, gabesaba.

- tenzen-y on [#7570](https://github.com/kubernetes-sigs/kueue/pull/7570#discussion_r2543766166): "This is a typical object reference pattern which should have a `ref` suffix."
- mimowo on [#8971](https://github.com/kubernetes-sigs/kueue/pull/8971#discussion_r2888547079): "maybe instead of `boolean` we could have something more generic like enum `mode` with options like `Disabled` or `TokenReview`, we could then …"

### MR-G5 Validate new fields strictly, with markers, and test it

Add bounds (`MinItems=1`, `Minimum=1`) and small caps from the first release: relaxing
later is compatible, tightening is breaking (gate it, or mark ACTION REQUIRED). Use CRD
markers and CEL first and webhooks for the rest; the Configuration API is not a CRD and
takes no markers. Test markers and CEL in integration tests.

Raised by: mimowo, tenzen-y, sohankunkerkar, alculquicondor, gabesaba, ahg-g, kerthcet.

- mimowo on [#17559](https://github.com/kubernetes-sigs/kueue/pull/17559#discussion_r4387154358): "Probably `+kubebuilder:validation:Minimum=1`? Using =0 is no-op"
- tenzen-y on [#19348](https://github.com/kubernetes-sigs/kueue/pull/19348#discussion_r4667362613): "Can you implement CRD marker test? We can implement those in integration test."

### MR-G6 Keep existing users working across upgrades

Say what happens to objects created by the previous version. Keep cleanup code for old
state for two releases, keep a replaced flag or field working for one release, and make
a new default map to today's behaviour. New startup validation must load every config
that loads today. Treat renames of user-facing resources as breaking, in a dedicated PR.

Raised by: mimowo, tenzen-y, sohankunkerkar, alculquicondor, kerthcet.

- mimowo on [#8530](https://github.com/kubernetes-sigs/kueue/pull/8530#discussion_r2681240298): "we cannot remove this code in the PR, because upgrading Kueue will not work - nothing will remove the finalizers from pre-existing workloads. So we …"
- tenzen-y on [#9370](https://github.com/kubernetes-sigs/kueue/pull/9370#issuecomment-3936515271): "The problem is that if we decide to enable this by default now, we can not change the default (enabled -> disabled) in the future version because …"

## H. Tests

### MR-H1 Prove that every new test and branch can fail

Break the code each new test covers (revert the fix, or delete a line it exercises) and
check that a test goes red. For a gate-off test that keeps the old behaviour, force the new
gate check to its enabled value (for example `features.Enabled(features.X)` → `true`).
Say in the PR which assert fails without the change. Cover every new non-error path, and
check with the coverage report.

Raised by: mimowo, sohankunkerkar, mbobrovskyi, alculquicondor, gabesaba, ahg-g, kerthcet.

- mimowo on [#9670](https://github.com/kubernetes-sigs/kueue/pull/9670#discussion_r2953337768): "The test is pretty long - can you point which assert(s) fail without the change?"
- sohankunkerkar on [#17388](https://github.com/kubernetes-sigs/kueue/pull/17388#discussion_r4309100117): "But no test covers it: if I delete this line, every test still passes."

### MR-H2 Test at the cheapest level, through the entry point, in a realistic setup

Prefer unit over integration and integration over e2e when no real cluster is needed.
Test through `Reconcile`, predicates, `schedule()` or `Validate()`, not inner helpers. Let
the controller under test set state, build inputs in their production shape, and use the
fake client with interceptors in place of hand-written stubs.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, alculquicondor, gabesaba, ahg-g.

- mimowo on [#18449](https://github.com/kubernetes-sigs/kueue/pull/18449#discussion_r4496749594): "Yes, the test is pretty useless, test rather the predicates Update / Create / Delete, or Reconciler - higher level abstractions, not the inner …"
- mimowo on [#7341](https://github.com/kubernetes-sigs/kueue/pull/7341#discussion_r2473657760): "Instead of changing the assert I prefer to change the setup to more realistic."

### MR-H3 [simple] Extend the existing table test

Add a row to the existing table of the production function, and keep the existing cases
unchanged next to the new one. Name test functions `Test<Func>`.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, alculquicondor, ahg-g.

- tenzen-y on [#13885](https://github.com/kubernetes-sigs/kueue/pull/13885#discussion_r3884162340): "We should not implement such tests. We should add new case to the existing TestScheduleForTAS instead."
- mimowo on [#10623](https://github.com/kubernetes-sigs/kueue/pull/10623#discussion_r3112361724): "Let's add a new test case rather than modifying the existing one"

### MR-H4 [simple] Keep the table shape standard and lean

Write tables as `map[string]struct{...}` named `testCases`, setup fields first and
`want*` literals last. Set gates with a per-case `featureGates` map. Compare whole objects
with `cmp.Diff`, and drop fields that are the same in every row.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, alculquicondor, gabesaba, kerthcet.

- mimowo on [#11208](https://github.com/kubernetes-sigs/kueue/pull/11208#discussion_r3430640556): "nit, use the featureGates map pattern, it is more flexitble than `featureGateDisabled` which may run into conflicts when we have more gates."
- sohankunkerkar on [#17451](https://github.com/kubernetes-sigs/kueue/pull/17451#discussion_r4339899202): "could you drop these wantEvents: nil lines (4 of them)?"

### MR-H5 [simple] Compare errors by identity

Assert `cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors())` against sentinel or typed
errors, as the Coding Guidelines say, in place of message substrings or a `wantErr bool`.

Raised by: mimowo, tenzen-y, mbobrovskyi, alculquicondor.

- tenzen-y on [#12293](https://github.com/kubernetes-sigs/kueue/pull/12293#discussion_r3429256054): "This is untipattern. So, can you use error comparision pattern? We can introduce `wantError error` instead of `wantErrSubstring string`."
- mbobrovskyi on [#17437](https://github.com/kubernetes-sigs/kueue/pull/17437#discussion_r4322541524): "I'd prefer replacing them with sentinel errors and asserting with `cmpopts.EquateErrors()`."

### MR-H6 [simple] Explicit, minimal test objects

Build each case's objects inline with the `pkg/util/testing*` wrappers and literal
expectations (DAMP), in place of file-local builders and closures. Set only the fields
the case exercises, with values that tell the feature apart (limit ≠ quota, non-default).

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, alculquicondor, gabesaba, ahg-g, kerthcet.

- tenzen-y on [#15648](https://github.com/kubernetes-sigs/kueue/pull/15648#discussion_r4040383057): "We should intentioanally implement duplicated codes in test so that we can fail when the prod code sprcification is far from test codes."
- kerthcet on [#1225](https://github.com/kubernetes-sigs/kueue/pull/1225#discussion_r1369685781): "Can you make Max different with Default?"

### MR-H7 [simple] One `ginkgo.It` per scenario

In integration and e2e tests, write each scenario as its own `It`. Keep
`DescribeTable` for webhook and CRD validation tests.

Raised by: mimowo, tenzen-y, mbobrovskyi, ahg-g.

- mimowo on [#17803](https://github.com/kubernetes-sigs/kueue/pull/17803#discussion_r4389062965): "Don't use DescribeTable, add two It tests."
- tenzen-y on [#19514](https://github.com/kubernetes-sigs/kueue/pull/19514#issuecomment-7138167852): "We typicall use DescribeTable for webhook and CRD marker test since it is actually almost Unit Test."

### MR-H8 [simple] Ginkgo steps, timeouts and cleanup

Write descriptive `ginkgo.By` steps, use the `behavioral` timeout constants (smallest
that works), and register `ginkgo.DeferCleanup` right after each create, or clean up in
`AfterEach`, in place of Go `defer` or steps at the end of an `It`.

Raised by: mimowo, mbobrovskyi, alculquicondor, ahg-g.

- mimowo on [#18223](https://github.com/kubernetes-sigs/kueue/pull/18223#discussion_r4484027211): "Can you add descripive ginkgo.By steps for each of the steps?"
- mimowo on [#9742](https://github.com/kubernetes-sigs/kueue/pull/9742#discussion_r3219423734): "Instead of defer, can we delete the slices in AfterEach / AfterAll as we normally do for other resources?"

### MR-H9 [simple] Debuggable assertions

Inside `Eventually`, take `g gomega.Gomega` and assert with `g.Expect`. Compare objects,
sets or conditions with the `pkg/util/testing` matchers in place of booleans. Wrap
mutating calls in `Eventually`, reuse the `behavioral` helpers, and start shared
assertion helpers with `ginkgo.GinkgoHelper()`.

Raised by: mimowo, tenzen-y, mbobrovskyi, alculquicondor, ahg-g.

- tenzen-y on [#3026](https://github.com/kubernetes-sigs/kueue/pull/3026#discussion_r1805313707): "Could you use the propagated gomega instance so that we can easily identify the error place during debug?"
- mimowo on [#12045](https://github.com/kubernetes-sigs/kueue/pull/12045#discussion_r3469528387): "avoid asserting on bools because they are hard to debug."

### MR-H10 Test both gate states, and scope test state

Keep cases for gate on and gate off while the gate exists, and name the case after the
state. Set a gate only in the cases that need it (`SetFeatureGateDuringTest`), and leave
default-on gates alone. Use a fake clock and `t.Setenv`, and create per-case state inside
`t.Run`.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, alculquicondor, kerthcet.

- tenzen-y on [#6973](https://github.com/kubernetes-sigs/kueue/pull/6973#discussion_r2375824093): "We should avoid enabling such Alpha feature at global level. We should set the FG only in the affected case."
- mbobrovskyi on [#13382](https://github.com/kubernetes-sigs/kueue/pull/13382#discussion_r4068706381): "Please mention in the test case name that `PodIntegrationVerifyRoleRequests` is disabled. Also, add a test case with this feature enabled."

### MR-H11 Show new tests are stable, and fix flakes at the root

Run new or changed integration and e2e tests 50 to 200 times and post the result. For a
flake, find the root cause in the logs and fix the test; keep production code and
timeouts as they are. Make ordering deterministic with priorities or steps.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, gabesaba.

- mimowo on [#9331](https://github.com/kubernetes-sigs/kueue/pull/9331#discussion_r2821734322): "Please run it locally or on CI 100 / 200 times"
- sohankunkerkar on [#9570](https://github.com/kubernetes-sigs/kueue/pull/9570#discussion_r2870449063): "I don't think this is the right fix. It will alleviate the symptom but not the root cause."

## I. PR, release note, review loop

### MR-I1 [simple] Release note and kind for users

Write `<Area>: <user-visible effect>` ("Fixed a bug where …"), name the gate and stage,
or `NONE` when no user can notice, inside the `release-note` block (see the
`kueue-release-notes` skill). Add `ACTION REQUIRED:` with the concrete step when admins
must act. Set `/kind` by user impact: a fix to an existing feature is `bug`.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, alculquicondor, gabesaba, kerthcet.

- mimowo on [#11877](https://github.com/kubernetes-sigs/kueue/pull/11877#issuecomment-4647960890): "please update the release note, the prefix should always be the feature / area, not "fix" ."
- mimowo on [#17859](https://github.com/kubernetes-sigs/kueue/pull/17859#issuecomment-6463821637): "For action required I would expect recommendation what admins need to do."

### MR-I2 [simple] Describe the problem, the choice and the evidence

State the user-facing problem, the options you weighed and why you chose this one, and
how you verified it (test levels, commands, a run on a real cluster for API-server
behaviour) under "Useful notes for your reviewer".

Raised by: mimowo, sohankunkerkar, mbobrovskyi, alculquicondor, gabesaba, ahg-g.

- mimowo on [#9211](https://github.com/kubernetes-sigs/kueue/pull/9211#issuecomment-3906940450): "Could you describe what the user-facing problem is with the old code?"
- mbobrovskyi on [#18387](https://github.com/kubernetes-sigs/kueue/pull/18387#issuecomment-6667818380): "could you add the test evidence to "Useful notes for your reviewer"?"

### MR-I3 [simple] Accurate title and issue links

Make the title match the final diff, with an area prefix. Write `Fixes #N` only when the
PR fully resolves the issue, otherwise `Part of #N`. Keep the PR template intact, since
release automation parses it.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, alculquicondor.

- mimowo on [#5678](https://github.com/kubernetes-sigs/kueue/pull/5678#issuecomment-3005539322): "Instead of Fixes please use prefix "Part of" or "KEP for", because "Fixes" is recognized by the robot which would auto-close the entire"
- mbobrovskyi on [#18130](https://github.com/kubernetes-sigs/kueue/pull/18130#issuecomment-6626256408): "Please update the PR title and description: tone_instructions and AGENTS.md are no longer changed."

### MR-I4 [simple] Clean commit messages and history

Put issue links and mentions in the PR body; commit messages carry plain text only, with
no `#N`, `Fixes` or AI co-author trailer. During review, add commits so the reviewer sees
what changed; squash when asked (for a cherry-pick or a `needs-rebase` from interim
commits).

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, kerthcet.

- tenzen-y on [#16220](https://github.com/kubernetes-sigs/kueue/pull/16220#issuecomment-5835739024): "Using "#" in commit messages is banned by the Kubernetes Organization."
- kerthcet on [#421](https://github.com/kubernetes-sigs/kueue/pull/421#issuecomment-1303234855): "better to append the commits not squashing them, it's hard to see what changes after. We can squash at the last."

### MR-I5 Plan cherry-picks

A user-observable bug fix gets a release note and cherry-picks to the supported branches
it affects. Say which branches need it, check that its tests compile there, and prepare
picks with `hack/cherry_pick_pull.sh` when the bot fails. A pick carries only the
original change.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi.

- mimowo on [#8071](https://github.com/kubernetes-sigs/kueue/pull/8071#issuecomment-3612389291): "If this is user-observable we need release note and cherrypick, right?"
- sohankunkerkar on [#18795](https://github.com/kubernetes-sigs/kueue/pull/18795#issuecomment-6889604731): "The fix matches main, but the test won't compile on release-0.19"

### MR-I6 [simple] Classify a CI failure before retesting

Check whether the failure relates to your change. For a flake, link the matching
`kind/flake` issue (or open one with the prow log) in the comment that carries `/retest`.

Raised by: mimowo, tenzen-y, alculquicondor, ahg-g.

- alculquicondor on [#1960](https://github.com/kubernetes-sigs/kueue/pull/1960#issuecomment-2057008400): "If the above error is a flaky, please open an issue."
- mimowo on [#11008](https://github.com/kubernetes-sigs/kueue/pull/11008#issuecomment-4443458496): "please check if rhis is releated, or a known flake"

### MR-I7 [simple] Close the review loop and stay rebased

Reply to every thread ("done" is enough for simple ones), resolve it after the fix is
pushed, and address all reviewers before asking again. Rebase when the PR conflicts or a
prerequisite merges, without waiting to be asked.

Raised by: mimowo, tenzen-y, sohankunkerkar, mbobrovskyi, alculquicondor, ahg-g.

- mimowo on [#12412](https://github.com/kubernetes-sigs/kueue/pull/12412#issuecomment-4791368688): "please make sure all comments are addressed - a small comment "done/fixed" will do for the simpler ones, and marking as resolved."
- tenzen-y on [#14348](https://github.com/kubernetes-sigs/kueue/pull/14348#issuecomment-5305697365): "can you rebase this PR onto the main branch since I fixed the main branch CI issue"

### MR-I8 Own the PR and its replies

Disclose AI use in the PR and in comments that rely on it. Use AI for research, then
write the reply yourself. For a bug found by tooling, say whether a real workload hit it.

Raised by: mimowo, sohankunkerkar.

- mimowo on [#12045](https://github.com/kubernetes-sigs/kueue/pull/12045#discussion_r3414606060): "instead of drafting the response, use AI for brainstorming, knowledge collection etc. Then write the response by yourself"
- mimowo on [#18604](https://github.com/kubernetes-sigs/kueue/pull/18604#issuecomment-6922944113): "Do you have a use case for this check on your platform, or this is just AI found issue?"

## J. KEPs

### MR-J1 A KEP describes the design, not the code

Keep Summary and Proposal high-level, and put the API as Go types with `json` tags in
Design Details. Leave code, code layout and pointers out (they go in the PR). Put
rejected options in Alternatives.

Raised by: mimowo, tenzen-y, sohankunkerkar, alculquicondor, kerthcet.

- mimowo on [#14616](https://github.com/kubernetes-sigs/kueue/pull/14616#discussion_r4047642753): "I think this is really too detailed for proposal. Proposal is a high level technical direction for the enhancement."
- tenzen-y on [#13163](https://github.com/kubernetes-sigs/kueue/pull/13163#discussion_r3789138451): "We should also add the Go API proposal after this sentence, which has the final Go API surface."

### MR-J2 Minimal Alpha, yes-or-no graduation criteria

Give every new field or mode a user story, and move the rest to future work. Write each
graduation criterion as a one-line bullet that can be answered yes or no, and turn every
deferred question into a Beta criterion ("re-evaluate X based on feedback").

Raised by: mimowo, tenzen-y, sohankunkerkar, alculquicondor, ahg-g, kerthcet.

- tenzen-y on [#18131](https://github.com/kubernetes-sigs/kueue/pull/18131#discussion_r4473249977): "The graduation criteria should be one line bullet point per a single criteria so that we can answer those w/ YES or NO when we evaluate graduation."
- mimowo on [#10796](https://github.com/kubernetes-sigs/kueue/pull/10796#discussion_r3170279013): "add apoint to graduation criteria to re evaluate the approach before going to beta based on user feedback"

### MR-J3 Cover interactions, limitations and what the user sees

List how the feature interacts with TAS, MultiKueue, Fair Sharing, WaitForPodsReady,
ProvisioningRequest and DRA. For every rejection or unsupported combination, say what
the user sees, and record accepted gaps in Risks or Drawbacks with the workaround.

Raised by: tenzen-y, sohankunkerkar, gabesaba, ahg-g.

- sohankunkerkar on [#14589](https://github.com/kubernetes-sigs/kueue/pull/14589#discussion_r4396897769): "Can the KEP list how this interacts with Fair Sharing's candidate waves, PriorityBoost, ProvisioningRequest and WaitForPodsReady?"
- tenzen-y on [#16188](https://github.com/kubernetes-sigs/kueue/pull/16188#issuecomment-5873127256): "The only my request here is to mention the following limitations in the KEP"

### MR-J4 [simple] Keep `kep.yaml` current

Set `latest-milestone` and the current stage's milestone to the target release, leave
later stages empty, declare the gates there, and list every reviewer so far.

Raised by: mimowo, tenzen-y, sohankunkerkar.

- sohankunkerkar on [#16535](https://github.com/kubernetes-sigs/kueue/pull/16535#discussion_r4208624261): "Can you add latest-milestone and milestone.alpha?"
- tenzen-y on [#12776](https://github.com/kubernetes-sigs/kueue/pull/12776#discussion_r3867069396): "Let's make the future stage empty."

## Where reviewers differ

Conflicts are resolved for the current maintainers (mimowo, tenzen-y, sohankunkerkar,
mbobrovskyi) and the newest comments, then today's code, linters and guidelines.

### Code and tests

- **`DescribeTable`** (MR-H7). Unit tests are Go tables; integration and e2e tests use one
  `It` per scenario, with `DescribeTable` for webhook and CRD validation only
  (`writing_tests.md`, mimowo and tenzen-y in 2026).
- **Test helpers** (MR-H6). gabesaba's 2025 "DRY helpers" ask closed in favour of DAMP
  cases. Shared, reusable assertions still go to `test/util` with `GinkgoHelper()`.
- **Error asserts** (MR-H5). Older kueuectl tests compared `err.Error()` strings; since the
  2026-11 Coding Guidelines, compare by identity everywhere.
- **Log levels** (MR-E1). Older asks for V(2) on routine lines gave way to the
  `logging_policy.md` table.
- **Panic or log** (MR-E4). alculquicondor panicked on any programmer error; mimowo limits
  `panic` to internal call-site contracts. Cluster-state anomalies are logged, and user
  input is validated.
- **Pointers.** The `forbidigo` linter rejects `ptr.To`; write `new(expr)` on main and
  `ptr.To` only in picks to Go 1.25 branches. `ptr.Deref` stays.

### Docs, gates and process

- **Stage words in comments** (MR-D4). sohankunkerkar drops "Alpha" from internal code
  comments; mimowo asks gated API fields and annotations to name their gate. Both hold.
- **Gate check placement** (MR-G1). Check where main and sibling code already check, with
  an explicit `features.Enabled(...)`, so a search for the gate finds every path.
- **`ACTION REQUIRED`** (MR-I1). tenzen-y asks for it when log keys or levels change;
  mimowo wants a concrete admin step. Add it when admins must act (dashboards and log
  queries included), and name the step.
- **Squashing** (MR-I4). Tide squash-merges, so split concerns into PRs. Add commits during
  review (kerthcet), and squash when a maintainer asks.
- **Backports** (MR-I5). New behaviour is no ban on a pick: say in the PR whether it should
  be cherry-picked. `RELEASE.md` allows minor Alpha improvements, and API or stage
  changes stay on main.
- **zh-CN** (MR-D6). Mirror mechanical fixes (mbobrovskyi); leave new prose to the zh-CN
  owners (mimowo).
- **Validation** (MR-G5). The 2022–2023 "webhooks, not CEL" and "strict on shipped
  fields" asks are replaced by markers and CEL first, and gated tightening.

## Not rules (historical)

These appear in the profiles but are not enforced today. Leave them out of reviews:

- License headers, typos, duplicated words and blank lines (kerthcet): the `goheader`,
  `misspell`, `dupword` and `revive` linters check them.
- Validation error messages as named constants (kerthcet): tests compare errors by type.
- Requests to depend only on released operator versions (kerthcet): `go.mod` pins
  pseudo-versions today.
- "Don't import `k8s.io/kubernetes`" (tenzen-y): the main module requires it since #13045.
- `/label tide/merge-method-squash` (alculquicondor): PRs squash on merge.
- Key order in the labels-and-annotations reference (mbobrovskyi): the page is generated
  and sorted.
- "No `Failed to` in error logs" and "`controller/` packages are imported only by main"
  (ahg-g): today's code does both.
