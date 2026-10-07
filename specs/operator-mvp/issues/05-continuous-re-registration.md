# Continuous re-registration

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

The operator's reason to exist over the chart's one-shot setup Job: registration state that a pod loses (rescheduled onto a fresh node, wiped storage) is restored automatically. Extend the registration planner beyond bootstrap to full diff semantics — every reconcile compares declared topology against observed `SHOW INSTANCES` and issues only the missing registrations. Leadership stays hands-off: the planner never emits a MAIN promotion when a MAIN already exists; failover belongs to the Raft coordinators.

Ensure the controller re-reconciles on relevant events (pod changes, periodic resync) so drift is detected without manual triggers. The demo: forcibly de-register or wipe a data instance, watch the operator converge the cluster back to fully registered with no human action.

## Acceptance criteria

- [ ] A data instance whose registration is lost is automatically re-registered on a subsequent reconcile
- [ ] A missing coordinator is automatically re-added
- [ ] A converged cluster produces zero commands on reconcile (verified no-op)
- [ ] No MAIN promotion is ever issued while a MAIN exists, including during recovery
- [ ] Planner unit tests cover: single lost registration, multiple lost, converged no-op, recovery with MAIN present
- [ ] E2E scenario: wipe one instance's registration state, assert the cluster converges back to fully registered

## Blocked by

- `03-bootstrap-registration.md`
- `04-kind-e2e-harness.md`
