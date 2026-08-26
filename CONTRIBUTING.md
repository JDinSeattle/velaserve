# Contributing

VelaServe is an evidence-gated systems experiment. Changes must preserve the distinction between simulation, real measurement, and conditional future implementation.

## Development contract

1. Open an issue describing the falsifiable behavior or defect.
2. Add a failing test before changing Go behavior or deployment safety checks.
3. Keep the preregistration immutable after real-GPU results are viewed. A new hypothesis requires a new versioned preregistration, not an edit to `z0-v1.yaml`.
4. Do not add production placement coordination, a coordination store, source budgets, or dispatch waves while the gate remains unrun.
5. Do not commit credentials, model weights, Terraform state, raw prompts, private keys, or unreviewed benchmark output.
6. Run `bash scripts/verify-stage1.sh` and include `.tools/verification.json` content in the pull-request description without committing that generated file.

## Evidence changes

Schema changes must update validators, JSON schemas, positive and negative fixtures, recorder/replay consumers, and the documented evidence version. Raw failures and cancellations are data: do not silently drop, retry, or relabel them. Derived metrics must name their calibration source and must not be presented as measured hardware counters.

## Commit and review scope

Prefer small commits with one behavior boundary. A review should verify the exact upstream pins, experiment cell pairing, fail-closed guards, artifact ledger behavior, and that ordinary upstream routing remains untouched. Cloud changes may render and validate locally but must not require AWS credentials in tests or CI.
