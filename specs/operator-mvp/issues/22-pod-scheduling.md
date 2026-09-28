# Pod scheduling

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md` — affinity and tolerations were listed there as parity roadmap, not MVP; this issue is that slice, scoped on 2026-09-28 from a survey of sixteen operators and charts.

## What to build

The `memgraph-high-availability` Helm chart's `affinity` block, restated in the shape the field converged on. `spec.scheduling` carries one rule the operator writes for the whole cluster and, per role, the scheduling surface of a `core/v1` PodSpec passed through as written.

`spec.scheduling.podAntiAffinity` is presence-based like every other optional block of the resource: present, both roles' pod templates carry one pod anti-affinity term selecting on the operator's identity labels; absent, none. Three knobs tune it, each a schema default so the empty block is the chart's default rule: `type` (`preferred`, weight 100, or `required`), `scope` (`role`, the selector carries the component label; `cluster`, it does not), `topologyKey` (`kubernetes.io/hostname`). The chart's three modes are three corners of that: its default is `preferred`/`role`, `parity` is `required`/`role`, `unique` is `required`/`cluster`. No surveyed operator generates a cross-role rule, so the two enums are the whole of what is invented here; the chart's `nodeSelection` mode with its three label knobs is a per-role `nodeSelector` and invents nothing.

Per role, `nodeSelector`, `tolerations`, `topologySpreadConstraints`, `podAntiAffinity` and `priorityClassName` land on that role's pod template verbatim, with two conveniences: a spread constraint naming no `labelSelector` is given the role's own pod selector, and the role's `podAntiAffinity` terms are appended to the operator's rule list by list, never merged into it and never replacing it, so a user adding a rule cannot silently lose the spread the cluster was created with. Both are the CloudNativePG shape (`enablePodAntiAffinity`, `topologyKey`, `podAntiAffinityType`, `additionalPodAntiAffinity`), chosen over the replace-wholesale designs (ECK, Vitess, Percona `advanced`) because those lose the default the moment a user writes anything.

Every field is mutable and every change is a pod-template change, which the existing rolling restart carries to the pods; nothing here is pinned and no controller code changes. The one design choice worth recording is the default: with the block presence-based, a minimal CR gets no rule, where the chart gave a soft one. A schema default cannot give it back without making the block impossible to remove, so the sample ships the block written out and the docs say what its absence costs.

Testing follows the house split. Builder tests pin every corner of the rule on both roles, the append-never-replace merge with and without the operator's rule, the per-role passthrough landing on its own role only, the selector fill on a spread constraint, and that a cluster without the block carries no scheduling field at all. Envtest covers admission: the block's knobs default, an absent block stays absent, the enums reject other values, the topology key must be a label key. No e2e: the multi-node kind cluster runs the default `preferred` rule implicitly through the sample, and asserting placement would make the suite depend on the node count.

## Acceptance criteria

- [ ] `spec.scheduling.podAntiAffinity` is an optional struct with `type`, `scope` and `topologyKey`, each a schema default; present it lands one term on both roles selecting on the identity labels (component label dropped at scope `cluster`); absent, no affinity
- [ ] `spec.scheduling.{coordinators,data}` carry `nodeSelector`, `tolerations`, `topologySpreadConstraints`, `podAntiAffinity` and `priorityClassName`, landing on that role's pod template only; an empty spread `labelSelector` is filled with the role's selector; role anti-affinity terms are appended after the operator's
- [ ] Builder tests cover the four rule corners, the merge with and without the rule, the passthrough and the no-block case
- [ ] Envtest covers defaulting, absence, the enums and the topology key pattern
- [ ] `make manifests generate chart-sync` regenerated, `make chart-verify` green, the CRD's compact JSON stays under the 256KB client-side apply limit
- [ ] `docs/scheduling.md`, the README and the sample describe the block, the chart mapping and what leaving the block out costs

## Blocked by

- `09-pod-tuning-knobs.md`: the per-role block shape and the pod template these fields join
- `17-sequenced-rolling-restart.md`: what carries a scheduling change to the pods
