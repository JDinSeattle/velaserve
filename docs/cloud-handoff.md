# AWS real-GPU Z0 handoff

This is the operator boundary. Repository code prepares images, manifests, calibrations, raw evidence, and fail-closed checks, but it never runs `terraform apply`, creates AWS resources, pushes images, or destroys infrastructure. The current public result remains **NOT RUN ON REAL GPU** until an operator completes this runbook.

## 1. Start from the frozen repository

Use a clean commit and create an ignored local artifact directory:

```bash
mkdir -p .artifacts
cp deploy/experiments/cloud.env.example /private/path/velaserve-cloud.env
source /private/path/velaserve-cloud.env
export VELASERVE_CONTROLLER_REVISION="$(git rev-parse HEAD)"
export VELASERVE_PREREGISTRATION_SHA256="$(shasum -a 256 "$VELASERVE_PREREGISTRATION_PATH" | awk '{print $1}')"
git status --short
```

Fill every blank in the private environment file. Never place AWS credentials, the Hugging Face token, the condition-control token, signing keys, or generated calibration artifacts in Git.

The first useful cloud run is `VELASERVE_Z0_PHASE=z0-b`, Arm B, one EPP replica, TCP, six homogeneous one-GPU replicas. A nonzero `VELASERVE_GROUP_LIMIT` is only a cheap plumbing smoke; it cannot produce a positive gate.

## 2. Operator-created infrastructure

Review `infra/terraform/terraform.tfvars.example`, the provider/module locks, IAM policies, networking, AMI, ECR repositories, versioned S3 bucket, Karpenter objects, GPU quota, and cost. Then run `terraform plan` and, only after review, the apply yourself. Configure `kubectl` for exactly that cluster and confirm the context before continuing.

The checked-in Terraform and scripts do not perform an apply or destroy. The GPU NodePool is scale-to-zero until the model StatefulSet is scheduled.

## 3. Build and publish four immutable images

From the clean commit, build the repository image plus the exact pinned EPP, routing sidecar, and vLLM observer builds:

```bash
./hack/build-velaserve-image.sh "$LOCAL_VELASERVE_IMAGE"
./hack/build-pinned-epp-image.sh "$LOCAL_EPP_IMAGE"
./hack/build-pinned-sidecar-image.sh "$LOCAL_ROUTING_SIDECAR_IMAGE"
./hack/build-pinned-vllm.sh "$LOCAL_VLLM_IMAGE"
```

Push them using your normal ECR workflow and fill the four digest-addressed `...@sha256:...` values in the private environment file. Preflight pulls the images and verifies their embedded source/patch/architecture labels, so a tag alone or a separately rebuilt image is rejected.

## 4. Deploy the pinned model and routing baseline

Install the pinned Gateway API, Gateway API Inference Extension, and Envoy Gateway versions listed in `docs/upstream-version-matrix.md`. Create the private model and condition-control Secrets yourself, then install the six-replica model runtime:

```bash
kubectl create namespace "$VELASERVE_NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
kubectl --namespace "$VELASERVE_NAMESPACE" create secret generic llm-d-hf-token \
  --from-literal=HF_TOKEN="$HF_TOKEN" --dry-run=client -o yaml | kubectl apply -f -
kubectl --namespace "$VELASERVE_NAMESPACE" create secret generic "$VELASERVE_CONDITION_CONTROL_SECRET_NAME" \
  --from-literal="$VELASERVE_CONDITION_CONTROL_SECRET_KEY=$VELASERVE_CONDITION_CONTROL_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f -

.tools/bin/helm upgrade --install "$VELASERVE_MODEL_RELEASE_NAME" deploy/model-runtime \
  --namespace "$VELASERVE_NAMESPACE" \
  --set replicas=6 \
  --set-string model.id="$VELASERVE_MODEL_ID" \
  --set-string model.revision="$VELASERVE_MODEL_REVISION" \
  --set-string images.vllm.repository="${VELASERVE_MODEL_IMAGE%@sha256:*}" \
  --set-string images.vllm.digest="sha256:${VELASERVE_MODEL_IMAGE##*@sha256:}" \
  --set-string images.routingSidecar.repository="${VELASERVE_ROUTING_SIDECAR_IMAGE%@sha256:*}" \
  --set-string images.routingSidecar.digest="sha256:${VELASERVE_ROUTING_SIDECAR_IMAGE##*@sha256:}"
```

Install the support chart with a provisional positive threshold. The crossover collector bypasses EPP scheduling, and the later invariant deliberately ignores only this one measured field.

```bash
./hack/prepare-pinned-router-chart.sh .artifacts/pinned-router
EPP_PATH_AND_REF="${VELASERVE_EPP_IMAGE#*/}"
.tools/bin/helm upgrade --install velaserve-support deploy/helm/velaserve \
  --namespace "$VELASERVE_NAMESPACE" \
  -f deploy/experiments/aws-z0-values.yaml \
  --set-string model.name="$VELASERVE_MODEL_ID" \
  --set model.minCachedTokenDelta=4096 \
  --set-string images.velaserve.repository="${VELASERVE_VELASERVE_IMAGE%@sha256:*}" \
  --set-string images.velaserve.digest="sha256:${VELASERVE_VELASERVE_IMAGE##*@sha256:}" \
  --set-string images.upstreamEPP.registry="${VELASERVE_EPP_IMAGE%%/*}" \
  --set-string images.upstreamEPP.repository="${EPP_PATH_AND_REF%%:*}" \
  --set-string images.upstreamEPP.tag="${EPP_PATH_AND_REF#*:}" \
  --set conditionController.enabled=true \
  --set-string conditionController.driverURL="$VELASERVE_CONDITION_DRIVER_ENDPOINT" \
  --set-string conditionController.revision="$VELASERVE_CONTROLLER_REVISION" \
  --set-string conditionControlSecret.name="$VELASERVE_CONDITION_CONTROL_SECRET_NAME"

kubectl --namespace "$VELASERVE_NAMESPACE" get configmap velaserve-upstream-router-values \
  -o 'jsonpath={.data.values\.yaml}' >.artifacts/router-values.yaml
.tools/bin/helm upgrade --install velaserve \
  .artifacts/pinned-router/config/charts/llm-d-router-standalone \
  --namespace "$VELASERVE_NAMESPACE" -f .artifacts/router-values.yaml
kubectl apply -f deploy/gateway/httproute.yaml
```

If your ECR repository has an additional path segment or tag punctuation, provide `images.upstreamEPP.registry/repository/tag` explicitly rather than relying on the simple shell split. Inspect both Helm renders and the actual Pod image strings before proceeding.

## 5. Run the executable calibrations

The calibration chain has no AWS API calls. It uses only the current `kubectl` context and local Go tools.

First collect forced recompute/P2P trials from every model Pod. The script opens temporary per-Pod tunnels, captures the patched vLLM runtime stream, and seals raw observations plus normalized evidence:

```bash
./hack/cloud-calibrate-crossover.sh
./hack/cloud-freeze-profile.sh
export VELASERVE_PROFILE_CALIBRATION_SHA256="$(shasum -a 256 "$VELASERVE_PROFILE_CALIBRATION" | awk '{print $1}')"
```

Apply the generated measured `minCachedTokenDelta`, regenerate the upstream values ConfigMap, and upgrade the router. All other EPP configuration must remain identical to the calibration deployment.

```bash
.tools/bin/helm upgrade velaserve-support deploy/helm/velaserve \
  --namespace "$VELASERVE_NAMESPACE" \
  -f deploy/experiments/aws-z0-values.yaml \
  -f "$VELASERVE_ROUTER_CALIBRATION_VALUES" \
  --reuse-values
kubectl --namespace "$VELASERVE_NAMESPACE" get configmap velaserve-upstream-router-values \
  -o 'jsonpath={.data.values\.yaml}' >.artifacts/router-values-final.yaml
.tools/bin/helm upgrade velaserve \
  .artifacts/pinned-router/config/charts/llm-d-router-standalone \
  --namespace "$VELASERVE_NAMESPACE" -f .artifacts/router-values-final.yaml
```

In a second terminal, start the two local tunnels and copy the printed in-cluster Gateway URL into `VELASERVE_GATEWAY_CHAT_URL`:

```bash
source /private/path/velaserve-cloud.env
./hack/cloud-tunnels.sh
```

Back in the operator terminal, run the saturation sweep. The default rate list must actually cross overload; if it does not, choose a wider strictly increasing list and fresh output paths. The collector retains stable in-cluster endpoint URLs while using temporary local metrics tunnels.

```bash
./hack/cloud-calibrate-load.sh
go run ./cmd/oracle-calibration \
  --profile "$VELASERVE_BENCHMARK_PROFILE" \
  --profile-calibration "$VELASERVE_PROFILE_CALIBRATION" \
  --crossover-bundle "$VELASERVE_CROSSOVER_BUNDLE" \
  --load-calibration "$VELASERVE_LOAD_CALIBRATION" \
  --inflight-publication-delay-ms "$VELASERVE_INFLIGHT_PUBLICATION_DELAY_MS" \
  --affinity-load-gate-seconds "$VELASERVE_AFFINITY_LOAD_GATE_SECONDS" \
  --output "$VELASERVE_ORACLE_CALIBRATION"
export VELASERVE_ORACLE_CALIBRATION_SHA256="$(shasum -a 256 "$VELASERVE_ORACLE_CALIBRATION" | awk '{print $1}')"
```

The oracle file is generated from the sealed raw crossover bundle and the raw load sweep. `prefill_tokens_per_second`, `pull_bytes_per_second`, `bytes_per_cached_token`, and output service times are not hand-entered.

## 6. Enable the condition driver

Generate exact stable per-Pod URLs, then upgrade the support chart with the raw load calibration and its derived profiles:

```bash
export VELASERVE_CONDITION_ENDPOINTS_JSON="$(./hack/cloud-condition-endpoints.sh)"
.tools/bin/helm upgrade velaserve-support deploy/helm/velaserve \
  --namespace "$VELASERVE_NAMESPACE" \
  --reuse-values \
  --set conditionDriver.enabled=true \
  --set-string conditionDriver.model="$VELASERVE_MODEL_ID" \
  --set-string conditionDriver.gatewayChatURL="$VELASERVE_GATEWAY_CHAT_URL" \
  --set-json conditionDriver.endpoints="$VELASERVE_CONDITION_ENDPOINTS_JSON" \
  --set-json conditionDriver.loadProfiles="$(jq -c . "$VELASERVE_LOAD_PROFILES")" \
  --set-json conditionDriver.loadCalibration="$(jq -c . "$VELASERVE_LOAD_CALIBRATION")" \
  --set-string conditionControlSecret.name="$VELASERVE_CONDITION_CONTROL_SECRET_NAME"
```

Wait for the model, EPP, Envoy, controller, driver, and clock-probe Pods to be ready with zero restarts. Keep `cloud-tunnels.sh` running.

## 7. Preflight, smoke, and real run

Fill the remaining image/controller/Envoy values in the private environment file, source it again, restore the generated hash exports, and run:

```bash
./hack/cloud-preflight.sh

# Cheap wiring check only; use a fresh artifact root.
export VELASERVE_GROUP_LIMIT=1
./hack/cloud-run-z0.sh

# Gate-eligible run: new artifact root, no limit.
unset VELASERVE_GROUP_LIMIT
export VELASERVE_ARTIFACT_ROOT="$PWD/benchmarks/raw/aws-z0-b-full-01"
./hack/cloud-run-z0.sh
```

`cloud-run-z0.sh` repeats preflight, binds the clean commit and exact deployment, streams raw EPP/inner-Envoy evidence for every EPP Pod, streams raw vLLM acquisition/transfer records for Z0-C, and writes a new append-only run root. Every workload group must receive a condition receipt proving reset, exact cache-owner state, and achieved load before benchmark traffic.

Successful preflight writes `.tools/cloud-preflight-binding.json`, a validated binding of the repository commit, immutable images, live Pod identities, routing configuration and calibration hashes. The run retains its initial binding as `$VELASERVE_ARTIFACT_ROOT/preflight-binding.json`. Collection compares that initial invariant with a fresh preflight binding and rejects deployment drift or restarts; keep both generated artifacts and do not edit them to reconcile a mismatch.

## 8. Collect and compile

After a run, collection re-runs preflight, rejects deployment drift or restart, normalizes raw streams, derives oracle/source-pressure records, seals the ledger, uploads to a new S3 run prefix, and verifies every remote object:

```bash
./hack/cloud-collect.sh
```

No positive result is authorized by a dashboard or a partial run. Compile and sign only complete retained bundles:

```bash
umask 077
go run ./cmd/velaserve-gate keygen --private "$PRIVATE_KEY_PATH" --public "$PUBLIC_KEY_PATH"
go run ./cmd/velaserve-gate compile \
  --bundles "$VELASERVE_ARTIFACT_ROOT" \
  --preregistration research/preregistration/z0-v1.yaml \
  --output-dir "$NEW_GATE_DIRECTORY" \
  --id "$DECISION_ID" \
  --private "$PRIVATE_KEY_PATH"
```

If Z0-B does not pass the placement branch, follow the compiler message and run the frozen Z0-C profile. Z0-C interleaves source counts 1/2/4 in one generated profile and requires the patched vLLM raw transfer stream; it does not accept operator-authored transfer JSON.

## 9. Cleanup boundary

Only after `cloud-collect.sh` prints the verified remote destination may you set the exact cleanup confirmation and run `hack/cloud-cleanup.sh`. That script removes only the declared workload releases/jobs unless the explicit GPU NodePool option is also set. EKS, VPC, IAM, ECR, S3, and Terraform state remain operator-owned. Review a separate Terraform destroy plan when the experiment is finished.

## What has and has not been tested

Repository-local Go, schema, render, pinned-source build, and Kind paths can be tested without AWS. This runbook itself is intentionally **not claimed as cloud-tested** in the repository; the operator supplies AWS identity, quota, cost approval, model access, image pushes, deployment, and real-GPU evidence.
