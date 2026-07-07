# Design: multi-resource dependencies & UX (v2 direction)

Outcome of a structured design grilling (2026-07-07). Twelve decisions, the
contradictions they created, how those were resolved, and the risks still
open. The current implementation (generic `DoResource`, Job-per-operation,
no references) is the v1 baseline this builds on.

## Implementation status (2026-07-07)

Build-order steps 1 and 4 are implemented and verified end-to-end on kind
against AWS (see `examples/`):

- **Graph engine (step 1)**: `spec.references` with templates, readiness
  gating (`WaitingForDependency`), hash-based propagation (upstream output
  changes re-patch dependents), blocking reverse-order teardown
  (`BlockedByDependents`), cycle detection (`CyclicReference`), and a
  graph-neighbor watch.
- **Composites (step 4, without revisions)**: cluster-scoped
  `DoCompositeDefinition` + namespaced `DoComposite`. Expressions
  (`${params.*}`, `${self.*}`, `${resources.*}`) render into child
  DoResources; sibling references compile into `spec.references`, so the
  graph engine provides ordering and teardown. Children are owned (GC) and
  pruned; status rolls up per child. **Deviation:** definition edits
  re-render all instances immediately — the decided revision/rollout
  machinery (#9) is future work, acceptable at current fleet sizes.
- Not yet implemented: typed CRDs + Provider CR (#1, #4), replacement
  engine & protect flags (#7, #8), writable plugin-cache volume (#10), gRPC
  runner (#11).

## The twelve decisions

### API shape & layering

1. **Generated typed CRDs** per provider resource (`BucketV2`,
   `BucketPolicy`, …) rather than only the generic `DoResource`. Wins:
   admission-time validation, `kubectl explain`, typed UX. Costs: codegen,
   CRD lifecycle, no union types (see #2).

2. **References live in a single `spec.references` block**, not sibling
   `*From` fields. Kubernetes structural schemas forbid `string | ref`
   unions and the Pulumi registry schema carries no cross-resource
   reference metadata, so refs are `{toPath, from: {kind, name, fieldPath},
   template?}` entries that write into an otherwise pure-registry-schema
   `spec.forProvider`. Paths are strings the schema cannot validate — the
   controller must validate them at admission/reconcile.

3. **A second, user-defined composite layer** (`CompositeDefinition` →
   platform-owned kinds like `Website`), XRD/kro-style. Expressions and
   cross-resource wiring are *first-class in composites*; raw typed CRDs
   keep only the minimal `spec.references` mechanism.

4. **Provider CR with runtime CRD generation** (`kind: Provider`,
   `spec: {package: aws@7.50.0, resources: [s3/*, ec2/Instance]}`).
   The operator fetches the registry schema and creates the selected CRDs.
   Requires cluster-scoped CRD-write RBAC and an explicit
   upgrade/conversion strategy when regenerating schemas in place.

### Dependency semantics

5. **Full propagation**: dependents hold indexed watches on their
   dependencies; ref re-resolution happens every reconcile; changed
   resolved values re-patch the cloud. The drift-recreate path (dependency
   replaced → new outputs) therefore heals dependents automatically.

6. **Blocking deletion**: a resource with live dependents keeps its
   finalizer until dependents are gone (reverse-topological teardown),
   surfaced as `Ready=False / BlockedByDependents` with the dependent list
   in status. Requires a reverse reference index.

7. **Auto-replacement with full cascade** when a propagated change hits an
   immutable input — bounded by #8.

8. **Guardrails: `protect` flag + create-before-delete ordering.**
   `protect: true` (default **on** for a curated stateful-type list: RDS,
   EBS, DynamoDB, …) turns required-replacement into a terminal
   `ReplacementRequired` condition needing explicit approval (annotation).
   Unprotected dependents replace create-before-delete whenever identity
   allows (avoids e.g. the no-bucket-policy security window);
   delete-before-create only as fallback for fixed-identity resources.

### Fleet & operations

9. **Composite revisions + progressive rollout.** Editing a
   `CompositeDefinition` creates an immutable revision; instances pin to
   revisions and migrate via rollout policy (manual / canary % /
   all-at-once). Template edits must never implicitly trigger a fleet-wide
   auto-replace sweep (interaction of #7 with 300 instances).

10. **Writable plugin-cache volume** resolves the conflict between
    runtime-chosen provider versions (#4) and pre-baked runner images:
    runner Jobs mount a shared PVC as `PULUMI_HOME`, install pinned plugins
    on demand under a per-plugin filesystem lock, and keep the root
    filesystem read-only. Owed: PVC lifecycle, RWX vs node-affinity choice,
    stale lock cleanup, download checksum verification, air-gap story. See
    `docs/PROVIDER_UX_IMPLEMENTATION.md`.

11. **Persistent per-provider runner service (gRPC)** replaces
    Job-per-operation once propagation fan-out matters (a shared VPC with
    500 dependents must not mean 500 pods). Warm plugin process, batching,
    10–100× faster reconciles; costs manager↔runner auth, a network API,
    and the loss of per-op pod isolation. Jobs remain the right model until
    the graph engine exists.

12. **Readiness gating** (implied by #5): a dependent whose refs don't
    resolve yet (dependency missing or not Ready) sets
    `Ready=False / WaitingForDependency` and requeues off the watch — no
    cloud call is attempted with placeholder values.

## Contradictions surfaced and their resolutions

- **Auto-replace vs blocking deletion**: replacement operates on the *same
  CR* (new external identity, same object), so the dependent-blocking
  finalizer rule doesn't apply to replacement; ordering safety comes from
  create-before-delete (#8), not from the deletion block.
- **Runtime provider versions vs baked plugins**: writable cache volume (#10).
- **Fan-out vs Job isolation**: gRPC runner service at scale (#11).
- **Fleet template edits vs auto-cascade**: revisions + rollout (#9).

## Risk register (grilled but unresolved — decide before building)

- ~~**Cycle detection**~~ *(done)*: cycles surface as a terminal
  `CyclicReference` condition; mutually-referencing resources in teardown
  break the deadlock via a deterministic name tie-break.
- **Namespace teardown deadlock**: blocking finalizers + namespace deletion
  can still wedge namespaces in `Terminating` if a dependent's cloud delete
  fails persistently. Needs an escape hatch (timeout, `force-delete`
  annotation) with an explicit orphaning contract.
- **fieldPath validity**: `status.outputs` paths are stringly-typed;
  typos surface only at reconcile. Consider validating against the
  resource's output schema at admission.
- ~~**Template/expression escaping**~~ *(done)*: `$${` escapes literals in
  composite expressions, `$${value}` escapes the reference-template
  placeholder, unterminated `${` is a render error, and map keys containing
  path metacharacters use bracket-quoted path segments.
- **Cross-namespace references**: allowed? If yes, RBAC story for reading
  other namespaces' status and the blast-radius implications.
- **Approval UX** (#8, #9): who approves `ReplacementRequired` at 3am; is
  approval an annotation, a CLI, or integration with something like
  Kargo/Argo.
- **CRD regeneration safety**: in-place schema updates on provider upgrade
  can invalidate stored objects; needs served/storage version policy and
  possibly conversion webhooks.
- **Connection secrets**: outputs containing credentials should optionally
  land in Secrets (`writeConnectionSecretToRef`-style), not in
  world-readable status.
- **Read-path credentials for the runner service**: a warm read service
  ideally uses read-only cloud credentials, distinct from mutate creds.
- **Secret inputs**: `spec.forProvider` values sourced from Secrets (not
  just other resources' outputs).

## Suggested build order (risk-first)

1. **Graph engine on the existing generic `DoResource`**:
   `spec.references`, indexed watches, readiness gating, blocking deletion,
   reverse index — prove propagation semantics without any codegen.
2. **Provider CR + typed CRD generation** (`forProvider` + `references`
   shape) + writable plugin-cache volume.
3. **Replacement engine**: immutability detection, protect flags, curated
   stateful list, create-before-delete, approval flow.
4. **Composites**: `CompositeDefinition`, expression language, revisions,
   progressive rollout.
5. **gRPC runner service** when reconcile volume demands it; keep Jobs as
   the fallback/audit path.
