# Bootstrap registration

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

Make a freshly provisioned cluster become an actual HA cluster without human action. Introduce the Memgraph HA client — a narrow Go interface (show instances, add coordinator, register instance, set main) over the Bolt driver, with all higher layers depending on the interface — and the registration planner: pure diff logic that takes declared topology plus observed `SHOW INSTANCES` output and returns the ordered commands needed (empty when converged).

Wire both into the controller: once pods are ready, query the coordinator leader, plan, and execute — adding coordinators, registering data instances, and issuing the initial MAIN promotion exactly once (only when no MAIN exists). All interaction is idempotent and read-before-write, so an operator restart mid-bootstrap is harmless. The demo: apply a CR, then `SHOW INSTANCES` on a coordinator shows every declared instance registered with one MAIN elected.

## Acceptance criteria

- [ ] A fresh `MemgraphCluster` converges to fully registered: all coordinators added, all data instances registered, one MAIN promoted
- [ ] MAIN promotion is issued only when no MAIN exists; an existing MAIN is never overridden
- [ ] Killing and restarting the operator mid-bootstrap still converges with no duplicate or failed registrations
- [ ] Planner covered by pure unit tests: fresh-cluster bootstrap, partially registered cluster, fully converged no-op, MAIN already present
- [ ] Controller registration flow covered by envtest with the HA client mocked behind its interface
- [ ] The HA client interface is the only place the Bolt driver is referenced

## Blocked by

- `02-provisioning-walking-skeleton.md`
