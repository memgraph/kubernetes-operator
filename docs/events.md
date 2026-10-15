# Events

The operator records a Kubernetes Event on the `MemgraphCluster` for every action it takes on the cluster. The Bolt commands are why: the Kubernetes audit log records every write the operator makes to the API server, but nothing it sends to Memgraph. Without Events, promoting a MAIN or unregistering an instance would show up only in the operator's log.

```sh
kubectl describe mgc my-cluster        # the Events section at the bottom
kubectl events --for mgc/my-cluster    # the same, as a list
```

Events are recorded through the `events.k8s.io/v1` API. Each carries a reason, an action and a note. Where the action touched a pod, a claim or a StatefulSet, that object is the Event's `related` object, so `kubectl describe` on it shows the Event too.

## Reasons

There is one reason per action, so alerts and event exporters can filter on it.

**Registration**, run on the coordinator leader. There is one Event per command the leader answers, in the order the commands ran:

| Reason | Type | When |
|---|---|---|
| `CoordinatorAdded` | Normal | `ADD COORDINATOR` was accepted |
| `InstanceRegistered` | Normal | `REGISTER INSTANCE` was accepted |
| `MainPromoted` | Normal | `SET INSTANCE ... TO MAIN` was accepted: at bootstrap, after a retiring MAIN was demoted, or when a refused handover restores MAIN to the instance it came from |
| `MainPromotionRefused` | Warning | The coordinators refused one promotion candidate. The operator then tries the next candidate, and a later `MainPromoted` names the instance that did become MAIN |
| `MainDemoted` | Normal | `DEMOTE INSTANCE` on a retiring MAIN was accepted |
| `InstanceUnregistered` | Normal | `UNREGISTER INSTANCE` on a retiring data instance was accepted |
| `CoordinatorRemoved` | Normal | `REMOVE COORDINATOR` on a retiring coordinator was accepted |
| `LeadershipYielded` | Normal | `YIELD LEADERSHIP` moved Raft leadership off a retiring coordinator |
| `BoltServerUpdated` | Normal | `UPDATE CONFIG FOR INSTANCE` or `UPDATE CONFIG FOR COORDINATOR` changed a member's announced `bolt_server` |
| `RegistrationFailed` | Warning | The leader refused any of the commands above except a promotion, with Memgraph's error |

**Settings**:

| Reason | Type | When |
|---|---|---|
| `SettingApplied` | Normal | `SET DATABASE SETTING` landed on a pod: a run-time flag, the license and organization, or an AWS credential |
| `SettingRejected` | Warning | A pod or the coordinators refused a `SET`, with Memgraph's error |
| `CoordinatorSettingApplied` | Normal | `SET COORDINATOR SETTING` landed |

**Kubernetes actions**:

| Reason | Type | When |
|---|---|---|
| `PodRestarted` | Normal | The roll deleted a pod to restart it onto the current template. The note says why that pod was next |
| `StatefulSetShrunk` | Normal | A lowered count shed the pods of members that had already left the cluster |
| `VolumeExpansionRequested` | Normal | A claim was patched to a grown size |
| `StatefulSetOrphaned` | Normal | A StatefulSet was deleted with its pods and claims orphaned, to be recreated with grown claim templates |

## What Events never carry

- **Secret values.** A setting read from a Secret (`enterprise.license`, `organization.name`, the four `aws.*` settings) is named in the note with "from the Secret", never with its value. A run-time flag's value comes from the spec and is shown.
- **Waiting.** A pass that is waiting on something records nothing: replication lag, a filesystem still growing, an address not yet assigned. Waiting is what the `Converged` and `Updated` conditions report, and an Event repeated every pass would bury the actions. A refused command does repeat while it keeps being retried. Kubernetes folds repeats of the same Event into one with a count.

## Keeping them

The API server keeps an Event for about an hour by default (`--event-ttl`). For a lasting record, run an event exporter or ship Events with your log pipeline, the same way as for every other controller's Events. The operator keeps no history of its own. Together with the Kubernetes audit log, which covers writes to the `MemgraphCluster` and the operator's own API calls, the Events give a complete record of who changed what.
