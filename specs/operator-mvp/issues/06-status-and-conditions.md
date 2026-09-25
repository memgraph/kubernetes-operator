# Status & conditions

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

Make cluster health inspectable without querying coordinators by hand. The CR status subresource reports the observed MAIN instance and standard conditions expressing convergence (all declared instances registered) and readiness. Status is observation only — it carries no secret material and is never used as reconcile input state.

Add printer columns so `kubectl get mgc` answers the everyday questions at a glance: coordinator count, data-instance count, current MAIN, ready/converged state, age. GitOps tooling and monitoring should be able to gate on the conditions.

## Acceptance criteria

- [ ] Status reports the currently observed MAIN instance and updates when failover changes it
- [ ] Conditions express convergence and readiness, transitioning correctly through bootstrap, converged, and degraded states
- [ ] `kubectl get mgc` shows counts, MAIN, readiness, and age via printer columns
- [ ] Status updates use the status subresource and never modify spec
- [ ] Envtest coverage: status reflects mocked cluster states (bootstrapping, converged, degraded)

## Blocked by

- `03-bootstrap-registration.md`
