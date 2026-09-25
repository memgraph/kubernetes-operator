# Quickstart example + README

**Type**: AFK

## Parent

`specs/operator-mvp/PRD.md`

## What to build

The minutes-to-cluster story for a developer evaluating Memgraph: a README walkthrough covering install (operator chart), a minimal `MemgraphCluster` example manifest (counts, image, license secret reference — everything else defaulted), and how to verify the cluster (`kubectl get mgc`, connecting over Bolt). Document the v1 contract honestly: counts are immutable, day-2 operations / external access / TLS / monitoring are not yet supported, and the operator never interferes with coordinator-driven failover. State the relationship to the HA Helm chart (operator is its successor; migration is fresh-cluster only, guide to come at parity).

## Acceptance criteria

- [ ] A newcomer can go from empty cluster to a registered, MAIN-elected Memgraph HA cluster following only the README
- [ ] The minimal example manifest works verbatim with only the license Secret substituted
- [ ] v1 limitations (immutable counts, deferred features) are stated explicitly
- [ ] Verification steps show expected `kubectl get mgc` output and a Bolt connection
- [ ] The example manifest is exercised in CI so it cannot rot

## Blocked by

- `04-kind-e2e-harness.md`
- `06-status-and-conditions.md`
