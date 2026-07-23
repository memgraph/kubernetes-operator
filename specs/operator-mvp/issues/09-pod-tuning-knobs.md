# Pod-tuning knobs

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

The remaining v1 configuration surface, per role, using the HA chart's vocabulary where concepts carry over: probe timings (startup, readiness, liveness — probe type stays fixed to the established TCP-socket convention), resource requests/limits, custom labels on pods/StatefulSets/Services, internal ports (bolt, management, replication, coordinator), the cluster domain used in advertised FQDNs, and a freeform non-secret env/args passthrough so any Memgraph flag is usable without a typed field.

Ports and cluster domain are the delicate part: they feed the ordinal-derived advertised addresses, so changing them must flow consistently through builders, registration planning, and the HA client's connection targets.

## Acceptance criteria

- [ ] Probe timings, resources, and labels are configurable per role and land on the right objects
- [ ] Internal ports and cluster domain are configurable and propagate consistently to container ports, Services, advertised addresses, and registration commands
- [ ] Non-secret env vars and extra args pass through per role; secret material remains only in the secrets block
- [ ] A CR with all knobs defaulted behaves identically to before this slice
- [ ] Builder golden tests cover each knob's default and override, including non-default ports/domain flowing into advertised addresses
- [ ] Planner unit tests confirm registration commands use the configured ports and domain

## Blocked by

- `02-provisioning-walking-skeleton.md`
