# Scheduling

`spec.scheduling` decides where the pods land. It replaces the `affinity` block of the `memgraph-high-availability` Helm chart with two things: one anti-affinity rule the operator writes for the whole cluster, and per role the scheduling surface of a pod in the `core/v1` vocabulary, passed through as written.

```yaml
spec:
  scheduling:
    podAntiAffinity:            # present: the operator keeps the pods of a role apart
      type: preferred           # preferred | required
      scope: role               # role | cluster
      topologyKey: kubernetes.io/hostname
    coordinators:
      nodeSelector: {}
      tolerations: []
      topologySpreadConstraints: []
      podAntiAffinity: {}       # the role's own terms, appended to the operator's rule
      priorityClassName: ""
    data:
      nodeSelector: {}
      tolerations: []
      topologySpreadConstraints: []
      podAntiAffinity: {}
      priorityClassName: ""
```

## The operator's rule

`podAntiAffinity` at the top of the block is presence-based, like every other optional block of the resource. Present, the operator writes one pod anti-affinity term into both roles' pod templates. Absent, it writes none, and the scheduler places the pods by free capacity alone: three coordinators on one emptier node is a legal outcome, and losing that node takes the Raft quorum with it. Leave the block out on a single-node cluster such as kind, where there is nothing to spread over, or when the per-role blocks carry a hand-written rule that should stand alone.

An empty block, `podAntiAffinity: {}`, is the chart's default: a *preferred* rule, weight 100, keeping the pods of each role on distinct nodes, that the scheduler ignores when nothing else fits. The three knobs inside tune it:

| Knob | Default | Alternative |
|---|---|---|
| `type` | `preferred`: a soft rule that never leaves a pod Pending | `required`: a pod with no node of its own stays Pending |
| `scope` | `role`: coordinators apart from coordinators, data instances apart from data instances | `cluster`: every pod of the cluster apart from every other |
| `topologyKey` | `kubernetes.io/hostname`: distinct nodes | any node label, such as `topology.kubernetes.io/zone` |

The chart's three modes are three corners of this table. Its default is `preferred` with scope `role`. Its `parity`, one coordinator and one data instance per node at most, is `required` with scope `role`. Its `unique`, one pod per node, is `required` with scope `cluster`, and needs at least `coordinators + dataInstances` nodes. The chart's `nodeSelection` mode is not a rule at all but a `nodeSelector` per role, below.

A `required` rule is a promise the cluster must be sized for. The e2e suite runs on a multi-node kind cluster and uses the default `preferred`, so it never depends on the node count.

The term selects on the operator's own identity labels, `app.kubernetes.io/instance` for the cluster and, at scope `role`, `app.kubernetes.io/component` for the role. Custom `spec.labels` cannot change those, so the rule cannot be detached from the pods it is meant for.

## Per role

The per-role blocks are the scheduling fields of a `core/v1` PodSpec, passed through as written and landing on that role's pod template only.

- `nodeSelector` pins a role to nodes carrying every one of its labels. The chart's `nodeSelection` mode, with its `roleLabelKey`, `coordinatorNodeLabelValue` and `dataNodeLabelValue` knobs, is `nodeSelector: {role: coordinator-node}` on the coordinators and `nodeSelector: {role: data-node}` on the data instances.
- `tolerations` let a role schedule onto tainted nodes.
- `topologySpreadConstraints` balance a role across a topology, which is the right tool for zones when there are more pods than zones: an anti-affinity over the zone label forbids or discourages sharing a zone at all, a spread constraint asks for the pods to be spread as evenly as the zones allow. A constraint that names no `labelSelector` is given the role's own pod selector, so `maxSkew`, `topologyKey` and `whenUnsatisfiable` are enough. A constraint naming its own selector keeps it.
- `podAntiAffinity` is the role's own anti-affinity in full `core/v1` form. Its terms are appended to the operator's rule, required list to required list and preferred list to preferred list, and never replace it: adding a rule of your own cannot silently lose the spread the cluster was created with. Without the operator's rule the role's terms are the whole affinity. There is no `podAffinity` or `nodeAffinity` passthrough yet; `nodeSelector` covers the node targeting the chart had.
- `priorityClassName` names the PriorityClass the role's pods run under.

## Changing it on a live cluster

Every field here is mutable, and every change is a pod-template change. Both StatefulSets use `updateStrategy: OnDelete`, so nothing but the operator ever restarts a pod: the change is applied to the StatefulSet at once and reaches the pods through the operator's [rolling restart](rolling-restart-pacing.md), one pod at a time, data instances before coordinators, each step gated on replication lag. The `Updated` condition reports the progress. A pod already running is never evicted for violating a rule it did not have when it was scheduled; the `IgnoredDuringExecution` half of every term is what the scheduler honours, and the roll is what brings the pods onto nodes that satisfy the new rule.

Two changes deserve a thought before they are made. Switching `type` to `required` on a cluster with too few nodes rolls the first data instance into Pending and stalls the roll there, with the cluster still serving on the remaining members; switch it back and the pod schedules. Adding a `nodeSelector` no node satisfies does the same.

## Not in scope

- **Generated cross-role rules beyond `scope: cluster`.** None of the sixteen operators surveyed for this design generates one either; the two-enum rule covers every mode the chart had.
- **`nodeAffinity` and `podAffinity` passthrough.** Not in the chart, and `nodeSelector` covers the targeting it had. Both are one field away if a case for them appears.
- **`schedulerName`** and **`podManagementPolicy`**. The former is exposed by six of sixteen surveyed operators and was not asked for; the latter belongs to the rolling restart.
