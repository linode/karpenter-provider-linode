# Chainsaw E2E Tests

This guide documents every Chainsaw E2E test in this repository, what each one validates, why it exists, and how to run suites via label selectors.

## How test selection works

This repo uses label selectors for `just run-e2e`:

- `just run-e2e` executes: `chainsaw test e2e --selector <selector> ...`
- Selector input is controlled by `CHAINSAW_SELECTOR` (default: `all`)
- Additional Chainsaw options can be passed through `CHAINSAW_FLAGS`

Key references:
- `run-e2e` command: `justfile`
- default selector and flags: `justfile`
- PR workflow selector (`pr`): `.github/workflows/e2e-pr.yaml`
- nightly workflow selector (`all`): `.github/workflows/e2e-nightly.yaml`

## Selector matrix

| Selector | Command | Runs | Typical use |
|---|---|---|---|
| `all` | `CHAINSAW_SELECTOR=all just run-e2e` | All Chainsaw suites | Full local run / nightly parity |
| `pr` | `CHAINSAW_SELECTOR=pr just run-e2e` | Fast PR-focused suites | Pre-PR validation |
| `nightly` | `CHAINSAW_SELECTOR=nightly just run-e2e` | Extended suites intended for periodic coverage | Longer confidence runs |
| `category.baseline` | `CHAINSAW_SELECTOR=category.baseline just run-e2e` | Baseline lifecycle behavior | Verify core provider health quickly |
| `category.scheduling` | `CHAINSAW_SELECTOR=category.scheduling just run-e2e` | Scheduling behavior suites | Debug pod placement / constraints |
| `category.consolidation` | `CHAINSAW_SELECTOR=category.consolidation just run-e2e` | Consolidation behavior suites | Debug scale-down/replacement behavior |
| `category.drift` | `CHAINSAW_SELECTOR=category.drift just run-e2e` | Drift behavior suites | Debug node replacement from config drift |

## Test inventory and justification

| Test | Path | Selector groups | What it validates | Why we need it |
|---|---|---|---|---|
| minimal-lke-standard | `e2e/minimal-lke-standard/chainsaw-test.yaml` | `all`, `pr`, `nightly`, `category.baseline` | Baseline scale-up/scale-down behavior with NodeClaim lifecycle and instance-type transitions under workload changes | Catches broad regressions in core provisioning/reconciliation path and verifies cluster can recover back to zero |
| scheduling-nodepool-label-selector | `e2e/scheduling-nodepool-label-selector/chainsaw-test.yaml` | `all`, `pr`, `nightly`, `category.scheduling` | NodePool label targeting via pod `nodeSelector`, and confirms pods land on expected node/nodepool | Ensures constraint-based scheduling works and avoids silent misplacement of workloads |
| scheduling-taints-tolerations | `e2e/scheduling-taints-tolerations/chainsaw-test.yaml` | `all`, `pr`, `nightly`, `category.scheduling` | Taint/toleration behavior with one tolerated and one non-tolerated workload | Prevents regressions where taints are ignored or non-tolerated pods are scheduled incorrectly |
| consolidation-empty-node-scale-down | `e2e/consolidation-empty-node-scale-down/chainsaw-test.yaml` | `all`, `pr`, `nightly`, `category.consolidation` | NodeClaim count reduction on a dedicated consolidation NodePool after scale-down | Validates empty/underutilized consolidation behavior with deterministic pool constraints |
| consolidation-non-empty-step-scale | `e2e/consolidation-non-empty-step-scale/chainsaw-test.yaml` | `all`, `nightly`, `category.consolidation` | Non-empty replacement signal on a dedicated consolidation NodePool after stepped scale changes | Covers disruptive consolidation edge cases while avoiding default-pool sizing bias |
| drift-nodepool-labels-taints | `e2e/drift-nodepool-labels-taints/chainsaw-test.yaml` | `all`, `nightly`, `category.drift` | Drift-triggered replacement when a drift-specific NodePool template labels/taints change | Ensures NodePool template mutations propagate through replacement in an isolated test-owned pool |

## Running tests

### Run selector-based suites (recommended)

```bash
# Full suite
CHAINSAW_SELECTOR=all just run-e2e

# PR subset
CHAINSAW_SELECTOR=pr just run-e2e

# Category-focused debug
CHAINSAW_SELECTOR=category.drift just run-e2e
```

### Run one specific test directory directly (debug convenience)

Use direct Chainsaw invocation when you want exactly one test file without introducing single-test labels:

```bash
chainsaw test e2e/minimal-lke-standard --config .chainsaw.yaml
```

## Authoring and maintenance rules

When adding/updating an E2E test:

1. Add labels for execution scope and category in `metadata.labels`:
   - include `all`
   - include `pr` and/or `nightly`
   - include exactly one `category.*`
2. Keep manifests close to ownership:
   - put scenario-specific manifests next to that scenario's `chainsaw-test.yaml`
   - reserve `e2e/common/` for manifests reused by multiple scenarios
3. Keep test cleanup explicit to avoid cross-test leftovers.
4. Update this document:
   - add/update row in **Selector matrix** if a new selector is introduced
   - add/update row in **Test inventory and justification**
5. Ensure workflow selectors (`pr`/`all`) still match intended scope.
