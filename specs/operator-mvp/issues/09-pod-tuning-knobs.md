# Pod-tuning knobs

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

The remaining v1 configuration surface, per role, using the HA chart's vocabulary where concepts carry over: probe timings (startup, readiness, liveness — probe type stays fixed to the established TCP-socket convention), resource requests/limits, custom labels on pods/StatefulSets/Services, the cluster domain used in advertised FQDNs, and a freeform non-secret env/args passthrough so any Memgraph flag is usable without a typed field.

The cluster domain feeds the ordinal-derived advertised addresses, so changing it must flow consistently through builders, registration planning, and the HA client's connection targets. Internal ports are fixed across workloads, Services, and advertised addresses.

## Acceptance criteria

- [ ] Probe timings, resources, and labels are configurable per role and land on the right objects
- [ ] The cluster domain is configurable and propagates consistently; fixed internal ports are used by container ports, Services, advertised addresses, and registration commands
- [ ] Non-secret env vars and extra args pass through per role; secret material remains only in the secrets block
- [ ] A CR with all knobs defaulted behaves identically to before this slice
- [ ] Builder golden tests cover each knob's default and override, including a non-default domain flowing into advertised addresses
- [ ] Planner unit tests confirm registration commands use the fixed ports and configured domain

## Blocked by

- `02-provisioning-walking-skeleton.md`
