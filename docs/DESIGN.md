# Design: multi-resource dependencies & UX (v2 direction)

Outcome of a structured design grilling (2026-07-07). Twelve decisions, the
contradictions they created, how those were resolved, and the risks still
open. The current implementation (generic `DoResource`, Job-per-operation,
no references) is the v1 baseline this builds on.

> A second grilling (2026-07-10) redesigned the typed platform-API layer —
> see [Typed platform APIs (v3 direction)](#design-typed-platform-apis-v3-direction)
> below.

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
- **Since then (2026-07-08)** most of the remaining decisions landed:
  Provider CR with runtime typed-CRD generation (#1, #4 —
  `DoProvider.typedResources` + `DoCompositeDefinition.api`), replacement
  engine with protect flags and approval (#7, #8), composite revisions with
  update policy (#9), the writable plugin-cache volume (#10), and
  readiness gating (#12). The gRPC runner (#11) is designed
  (`docs/RUNNER_SERVICE_DESIGN.md`) but not built — Jobs remain the only
  executor.

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
- ~~**fieldPath validity**~~ *(done)*: unresolved `status.outputs.*` paths
  are validated against the source's provider output schema at reconcile —
  typos fail early as `InvalidReferences` before any cloud call (best
  effort; admission-time validation remains a possible refinement).
- ~~**Template/expression escaping**~~ *(done)*: `$${` escapes literals in
  composite expressions, `$${value}` escapes the reference-template
  placeholder, unterminated `${` is a render error, and map keys containing
  path metacharacters use bracket-quoted path segments.
- **Cross-namespace references**: allowed? If yes, RBAC story for reading
  other namespaces' status and the blast-radius implications.
- **Approval UX** (#8, #9) *(partially done)*: replacement approval is the
  `do.pulumi.com/approve-replacement=<generation>` annotation (plus
  `spec.protect: false` for opt-out); revision rollout is `updatePolicy` +
  `revisionRef`. Open: richer approval integrations (CLI, Kargo/Argo).
- **CRD regeneration safety**: in-place schema updates on provider upgrade
  can invalidate stored objects; needs served/storage version policy and
  possibly conversion webhooks.
- ~~**Connection secrets**~~ *(done)*: `spec.writeConnectionSecretToRef` +
  `spec.connectionDetails` publish selected outputs into a same-namespace,
  owner-referenced Secret.
- **Read-path credentials for the runner service**: a warm read service
  ideally uses read-only cloud credentials, distinct from mutate creds.
- ~~**Secret inputs**~~ *(done)*: `spec.valuesFrom` injects Secret values
  via kubelet env into runner pods; placeholder + mapping only in the
  object/op. Redaction covers the streamed log and the error message; the
  provider-assigned id is guarded (`SecretInputInID`, length-thresholded to
  avoid coincidental false positives) since it cannot be redacted without
  breaking later ops. Structured `status.outputs`/state are deliberately NOT
  redacted, so a provider that echoes an input round-trips the real value into
  `writeConnectionSecretToRef` instead of publishing `"(redacted)"` — outputs
  are already sensitive-adjacent in etcd, as provider-generated secrets are.
  Rotation mixes the Secret resourceVersion into both the applied hash and the
  runner Job name, so a rotated op re-patches and cannot adopt a stale-value
  completed Job. Components excluded (checkpoint would persist values).

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

---

# Design: typed platform APIs (v3 direction)

Outcome of a second structured design grilling (2026-07-10), focused on how
platform teams extend the Kubernetes API from composite definitions and how
app/project teams consume the result. Seven decisions. The baseline is the
current implementation: `DoCompositeDefinition.spec.api` → CRD in the fixed
`typed.do.pulumi.com` group (`internal/controller/typed_crd.go`) + runtime
translation controllers (`internal/controller/typed_controllers.go`).
Heavily informed by Crossplane v2 precedent, verified against their
docs/issues on 2026-07-10 (see [Precedent](#precedent-verified-2026-07-10)).

## The target mental model

- **Platform team**: one `DoCompositeDefinition` = resource templates + one
  parameter schema + `api: {group, version, kind}`. Apply it and doplane
  serves e.g. `websites.platform.acme.com/v1` — a real, versioned, branded
  Kubernetes API. Status conditions say it is served, or exactly why not.
- **App team**: applies the platform kind. Parameters live flat in `spec`,
  validated at admission; doplane's lifecycle knobs live under the reserved
  `spec.doplane` block. That object is the *only* thing app teams are taught.
- **Everything else** — `DoComposite`, revisions, child `DoResources` — is
  machinery: visible for debugging, absent from the user-facing story.

## Why: six fault lines in the current implementation

1. **The typed path is strictly weaker than the raw one.**
   `TypedCompositeReconciler` maps `spec` → `parameters` and nothing else,
   so typed users cannot set `updatePolicy` or pin a revision — the "nice"
   API the platform publishes is the less capable one.
2. **Three sources of truth for the parameter contract**:
   `requiredParameters` (flat list), `api.parametersSchema` (hand-written
   JSONSchemaProps blob, `PreserveUnknownFields`, unvalidated at authoring),
   and the `${params.*}` the templates actually reference. Nothing
   cross-checks any pair of them.
3. **No feedback surface.** `DoCompositeDefinitionStatus` carries only a
   composite count — no conditions. A malformed `parametersSchema` produces
   a Warning event and is terminal until the spec changes; `kubectl get docd`
   says nothing about whether the API is being served.
4. **Templates are revisioned; the API schema is not.** `applyCRD` updates
   the CRD in place, so a Manual-pinned instance renders old templates
   behind the current front-door schema.
5. **Kind-collision ownership is in-memory** (`TypedRegistrar.claim`).
   After a manager restart the owners map is empty; two definitions fighting
   over one plural re-race and the winner is nondeterministic.
6. **One fixed group, one frozen version** for every platform API in the
   cluster: no platform branding (`platform.acme.com`), no version
   evolution; the risk register's "CRD regeneration safety" was still open.

## The seven decisions

1. **Typed kinds are the product.** `DoComposite` is demoted to internal
   machinery, like `DoCompositeDefinitionRevision`: it remains a real object
   (debugging, escape hatch) but docs and examples teach the typed kind.
2. **One structured schema field** on the definition — typed
   `JSONSchemaProps`, not a raw JSON blob — validated when the definition is
   applied. `requiredParameters` is deleted. Render-time validation of the
   parameters and cross-checks of template `${params.*}` usage run against
   the same schema.
3. **Platform-chosen group + version** (`api.group`, `api.version`), gated
   by an **install-time group allowlist** (Helm value → enumerable RBAC
   rules). A definition naming an unlisted group gets a terminal
   `GroupNotAllowed` condition. A wildcard CR grant was rejected: the fixed
   group existed to keep manager RBAC enumerable, and that property is kept.
4. **Reserved `spec.doplane` block** on typed objects for doplane's knobs
   (`updatePolicy`, `revisionRef`), injected into every generated schema —
   the Crossplane v2 `spec.crossplane` move. Platform params live flat at
   the top level of `spec`; a definition declaring a param named `doplane`
   is rejected at apply time.
5. **Versioning follows the Crossplane model**: a definition may serve
   multiple versions, generated CRDs always use conversion strategy `None`,
   all served versions must be round-trippable (new *required* field = new
   API), exactly one version is referenced by the templates. On a version
   bump the old version stays served-and-deprecated, the definition's status
   reports stored-object counts per version, humans migrate manifests, and
   the old version is dropped only at zero. Webhook conversion was rejected:
   TLS/cert plumbing as an install dependency and webhook availability
   becoming API availability.
6. **Renames are forbidden.** `api.group` and `api` names are immutable
   once served (CEL), consistent with `spec.type` immutability on
   DoResource and with Crossplane (breaking change = new XRD). A rename is a
   new definition; migration happens at the leaf (decision 7's checklist).
   A composite-level "mirror adoption" handshake (transfer the typed CR →
   DoComposite owner reference) was considered and deferred — doplane's
   mirror layer makes it possible where Crossplane can't, but it is
   ownership-transfer code guarding live infrastructure; build it only on
   real demand.
7. **CRD ownership is persisted on the CRD itself** (managed-by label +
   owner annotation, checked before any apply) instead of the in-memory
   claim map. Fixes the restart re-race and prevents hijacking a CRD some
   other operator owns — mandatory once groups are platform-chosen.

Default set without objection: provider-resource typed CRDs
(`DoProvider.typedResources`) stay in the fixed `typed.do.pulumi.com` group —
they are doplane-shaped mirrors of Pulumi tokens, not platform brands. Only
composite APIs get platform groups.

## Contradictions surfaced and their resolutions

- **Platform-owned spec vs doplane knobs**: the platform schema owns `spec`,
  but typed parity needs `updatePolicy`/`revisionRef` somewhere → reserved
  `spec.doplane` block; annotations rejected (invisible to schema and
  `kubectl explain`), `spec.parameters` nesting rejected (reads as a doplane
  form, not a native API).
- **"Real versioning" vs conversion webhooks**: Kubernetes offers only
  `None` or `Webhook` → Crossplane's `None`/round-trippable ceiling adopted
  deliberately.
- **Platform groups vs enumerable manager RBAC** → install-time allowlist;
  adding a group is a values change + upgrade, which doubles as a deliberate
  platform decision.
- **Rename support vs live-infrastructure safety** → immutability; the
  adoption escape hatch lives at the DoResource leaf, not the composite.

## Implementation checklist (forced by the decisions)

Implemented 2026-07-10, in the same grilling's follow-up session:

- [x] `api` gains `group`/`version`; CEL immutability on group/names (plus
      "api cannot be removed once set"); allowlist check
      (`--composite-api-groups`) with terminal `GroupNotAllowed` condition;
      the `compositeApiGroups` Helm value renders both the flag and the
      per-group RBAC rules
- [x] structured parameters-schema field replaces the raw blob **and**
      `requiredParameters` (`api.parametersSchema` is a typed
      `JSONSchemaProps`, schemaless at the CRD layer); the apiserver's CRD
      validation is the authority at apply time (rejections surface as
      `InvalidSchema`); `${params.*}` usage cross-checked against declared
      properties; render-time validation of DoComposite parameters runs the
      same schema (`composite_params.go`)
- [x] `spec.doplane` injected into generated schemas;
      `TypedCompositeReconciler` maps it to `updatePolicy`/`revisionRef`;
      param named `doplane` rejected
- [x] status conditions on `DoCompositeDefinition` (`APIServed` with
      reasons `Served`/`InvalidSchema`/`GroupNotAllowed`/`CRDConflict`/
      `StoredVersionInUse`/`DeletionBlocked`) plus per-version object
      counts (`status.apiVersions`, attributed via managedFields)
- [x] CRD ownership label + `do.pulumi.com/owner` annotation with a
      pre-apply check; the in-memory `claim` map is gone (label-only CRDs
      from pre-annotation releases are adopted); superseded translation
      controllers fence themselves off via the registrar
- [x] definition finalizer (`do.pulumi.com/typed-api`) blocking deletion
      while typed CRs exist; at zero objects the generated CRD is deleted
      and the informer removed
- [x] external-name pass-through: `CompositeResourceTemplate.externalName`
      renders (params/self only) into the child's
      `crossplane.io/external-name` annotation
- [x] version-bump flow: `api.deprecatedVersions` keeps old versions
      served-and-deprecated; dropping a version with objects still stored
      is refused (`StoredVersionInUse`); at zero objects
      `status.storedVersions` is pruned automatically before the spec
      update (safe because conversion is `None` and schemas are
      round-trippable)

## Risk register (grilled and accepted)

- **Schema/template skew**: templates are revisioned, the API schema is not;
  a Manual-pinned instance renders old templates behind the current schema.
  Bounded by the round-trippability rule — accepted.
- **Migration ergonomics**: leaf-level orphan + external-name re-adoption is
  manual and per-resource, matching Crossplane's long-lived pain. Revisit
  mirror adoption (decision 6) only if real platform teams hit the wall.
- **Breaking existing `typed.do.pulumi.com` composite APIs**: acceptable at
  v1alpha1.

## Precedent (verified 2026-07-10)

Crossplane v2: an XRD's name must be `<plural>.<group>` and breaking changes
ship as a brand-new XRD (no rename); generated CRDs always use conversion
`None` — webhook conversion has never shipped
([crossplane#2608](https://github.com/crossplane/crossplane/issues/2608),
[crossplane#6964](https://github.com/crossplane/crossplane/issues/6964));
multiple served versions with exactly one `referenceable`, round-trippable
schemas required; `spec.crossplane` is the reserved-block precedent; v2
removed claims, so no XR-level binding/adoption layer exists. See the
[XRD docs](https://docs.crossplane.io/latest/composition/composite-resource-definitions/)
and [Upbound's XR API-evolution guidance](https://blog.upbound.io/crossplane-xr-best-practices).
