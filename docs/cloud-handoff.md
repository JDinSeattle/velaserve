# AWS real-GPU Z0 handoff

This runbook begins where repository verification ends. It intentionally does not automate `terraform apply`, model deployment, or `terraform destroy`. Those steps need the operator's AWS identity, budget, quota, AMI, model license, and final plan review.

## 1. Freeze the local handoff

From a clean checkout of the intended commit:

```bash
bash scripts/verify-stage1.sh
git status --short
git rev-parse HEAD
shasum -a 256 research/preregistration/z0-v1.yaml
```

Keep the commit and preregistration digest with the run notes. Do not run real evidence from a dirty preregistration or calibration file.

## 2. Review and create infrastructure

1. Confirm AWS account/region, EKS 1.34 availability, EC2 GPU quota, six-to-eight homogeneous instances, exact EKS-optimized AL2023 GPU AMI, expected NAT/EKS/GPU cost, and whether TCP or EFA will be measured.
2. Copy `infra/terraform/terraform.tfvars.example` to ignored `infra/terraform/terraform.tfvars` and replace every sample account-derived value, AMI, CIDR, and tag.
3. Configure a reviewed remote state backend for a persistent environment. The repository does not create one silently.
4. Run:

```bash
.tools/bin/terraform -chdir=infra/terraform init
.tools/bin/terraform -chdir=infra/terraform plan -out=velaserve-z0.tfplan
```

5. Inspect every resource, IAM policy, security group, public endpoint setting, ECR/S3 name, NAT gateway, module/provider version, Karpenter version, AMI, instance allowlist, and tag.
6. If and only if the plan matches the intended account and budget, the operator may run:

```bash
.tools/bin/terraform -chdir=infra/terraform apply velaserve-z0.tfplan
```

The repository's scripts do not invoke this command.

Configure kubectl from the exact Terraform output, then review and apply the rendered scale-to-zero Karpenter objects:

```bash
eval "$(.tools/bin/terraform -chdir=infra/terraform output -raw configure_kubectl_command)"
.tools/bin/terraform -chdir=infra/terraform output -raw karpenter_gpu_ec2_node_class_yaml | kubectl apply -f -
.tools/bin/terraform -chdir=infra/terraform output -raw karpenter_gpu_node_pool_yaml | kubectl apply -f -
```

The NodePool launches no node until matching GPU pods are pending. Confirm the exact cluster/context after `eval`; never run later steps against a similarly named cluster.

## 3. Publish immutable images

Build VelaServe from the verified commit. Build EPP with the repository command below: it exports commit `ab723b898f8598ab6631e9848a4cf28accd9b9ea` into a temporary tree, checks/applies the observational-only Stage-1 patch, and compiles that exact source. Do not build a moving upstream branch or hand-edit the fetched checkout.

```bash
docker build -t "$VELASERVE_LOCAL_IMAGE" .
./hack/build-pinned-epp.sh dist/cloud/epp linux amd64
docker build -t "$EPP_LOCAL_IMAGE" -f deploy/kind/Dockerfile.epp dist/cloud
```

Push both images to the Terraform-created immutable ECR repositories. Resolve and record full references of the form:

```text
ACCOUNT.dkr.ecr.REGION.amazonaws.com/REPOSITORY@sha256:DIGEST
```

Tags alone are not accepted by preflight. Cloud preflight queries ECR to ensure each digest exists in the declared account and region.

The upstream EPP chart renders its immutable image as `REPOSITORY:git-ab723b...@sha256:DIGEST`; record that exact tagged-digest string as the EPP pod image. The model and VelaServe/controller images may use the simpler `REPOSITORY@sha256:DIGEST` form.

## 4. Deploy the homogeneous model fleet

The operator deploys vLLM because model credentials, storage, image, and license are environment-specific. The real fleet contract is:

- one immutable model revision and tokenizer across all pods;
- six to eight ready pods on one approved instance type;
- one GPU per pod for the documented first shape;
- label `app.kubernetes.io/name=velaserve-model`;
- label `velaserve.ai/model-revision=IMMUTABLE_MODEL_REVISION` on every selected pod;
- Service `velaserve-model` in namespace `velaserve-z0`, port 8000;
- OpenAI-compatible `/v1/chat/completions`, `/metrics`, and the llm-d KV event/P2P integration required by the pinned router;
- consistent cache block/connector settings and declared `tcp` or `efa` transport;
- no autoscaling, restart, image update, or mixed hardware during an evidence cell.

The benchmark also requires an environment-specific condition driver. This is the only cloud-specific adapter the repository cannot infer. It must actually establish requested background load, cache ownership/distribution/coldness, and Z0-C prefix-source count, then return observed state rather than echoing the request. The controller sends the frozen condition request as JSON and requires this response shape:

```json
{
  "schema_version": "velaserve.applied-condition/v1",
  "load_regime": "moderate",
  "cache_state": "distributed-warm",
  "prefix_source_count": 2,
  "observed_state": {
    "offered_load_qps": 50,
    "achieved_load_qps": 50,
    "saturation_qps": 100,
    "cached_endpoint_ids": ["model-0", "model-1"],
    "prefix_source_endpoint_ids": ["model-0", "model-1"],
    "measurement_source": "operator-driver-v1"
  }
}
```

`prefix_source_count` and `prefix_source_endpoint_ids` are omitted outside Z0-C. The controller derives load validity from both offered/saturation and achieved/saturation QPS (idle 0–5%, moderate 40–60%, near saturation 85–95%), validates cold/warm/distributed cache ownership, and for Z0-C requires the exact source set to equal the cached set. Z0-C uses only the `distributed-warm` workload cell. The controller rejects an empty, mislabeled, or mismatched response, hashes the whole applied-state object, and attaches the observed values, digest, and controller revision to the group before inference starts. Deploy the driver as a private in-cluster Service; it is environment-owned and is not copied to public artifacts unless the operator chooses to publish it.

Render the support chart with `deploy/experiments/aws-z0-values.yaml`, the chosen Arm A/B values, and both exact digests. The upstream chart represents a digest-addressed image as `tag@sha256:digest` in its tag field:

```bash
.tools/bin/helm upgrade --install velaserve-support deploy/helm/velaserve \
  --namespace velaserve-z0 --create-namespace \
  -f deploy/experiments/aws-z0-values.yaml \
  -f deploy/experiments/arm-b-values.yaml \
  --set-string images.velaserve.repository="$VELASERVE_ECR_REPOSITORY" \
  --set-string images.velaserve.digest="sha256:$VELASERVE_IMAGE_DIGEST" \
  --set-string images.upstreamEPP.registry="$EPP_ECR_REGISTRY" \
  --set-string images.upstreamEPP.repository="$EPP_ECR_REPOSITORY" \
  --set-string images.upstreamEPP.tag="git-ab723b898f8598ab6631e9848a4cf28accd9b9ea@sha256:$EPP_IMAGE_DIGEST" \
  --set conditionController.enabled=true \
  --set-string conditionController.driverURL="http://velaserve-condition-driver.velaserve-z0.svc.cluster.local:8083/v1/apply" \
  --set-string conditionController.revision="$VELASERVE_CONTROLLER_REVISION"

kubectl --namespace velaserve-z0 get configmap velaserve-upstream-router-values \
  -o 'jsonpath={.data.values\.yaml}' >.tools/aws-router-values.yaml
.tools/bin/helm upgrade --install velaserve \
  .tools/upstream/llm-d-router/config/charts/llm-d-router-standalone \
  --namespace velaserve-z0 \
  -f .tools/aws-router-values.yaml
kubectl apply -f deploy/gateway/httproute.yaml
```

Inspect both renders before installing. `aws-z0-values.yaml` disables simfleet and points only at the declared real model service and label. Install the pinned Envoy Gateway and Gateway API Inference Extension releases before applying the route, using the versions in `docs/upstream-version-matrix.md`.

Keep a local tunnel to the in-cluster condition controller open while `cloud-run-z0.sh` executes:

```bash
kubectl --namespace velaserve-z0 port-forward service/velaserve-condition-controller 18082:8082
```

## 5. Produce a real calibration

Create an operator-owned YAML file with this strict shape, using values measured on the exact model/GPU/transport stack:

```yaml
prefix_tokens: 4096
inflight_publication_delay_ms: 5
affinity_load_gate_seconds: 2
calibration:
  prefill_tokens_per_second: 1000
  pull_bytes_per_second: 1000000
  bytes_per_cached_token: 512
  service_seconds: 1
```

The numbers above show the schema only; they are not an AWS calibration. Store the real file outside public Git if it contains environment details, hash it, and retain its measurement notes. The oracle will copy and hash it into the artifact bundle.

## 6. Export the guarded run environment

Set these values in the operator shell. Values must match Terraform outputs and the running workload exactly.

```bash
export VELASERVE_AWS_ACCOUNT_ID="$AWS_ACCOUNT_ID"
export VELASERVE_AWS_REGION="$AWS_REGION"
export VELASERVE_CLUSTER_NAME="velaserve-z0"
export VELASERVE_NAMESPACE="velaserve-z0"
export VELASERVE_ARTIFACT_BUCKET="$ARTIFACT_BUCKET"

export VELASERVE_GPU_QUOTA_CODE="$EC2_GPU_QUOTA_CODE"
export VELASERVE_GPU_QUOTA_REQUIRED="$REQUIRED_VCPU_QUOTA"
export VELASERVE_GPU_MINIMUM_BENCHMARK_REPLICAS="6"
export VELASERVE_GPU_MAXIMUM_BENCHMARK_REPLICAS="8"
export VELASERVE_GPU_INSTANCE_TYPES="g6e.xlarge"
export VELASERVE_P2P_TRANSPORT="tcp"

export VELASERVE_MODEL_SELECTOR="app.kubernetes.io/name=velaserve-model"
export VELASERVE_MODEL_CONTAINER_NAME="vllm"
export VELASERVE_MODEL_WORKLOAD_NAME="velaserve-model"
export VELASERVE_MODEL_ID="Qwen/Qwen3-8B"
export VELASERVE_MODEL_REVISION="$IMMUTABLE_MODEL_REVISION"
export VELASERVE_MODEL_IMAGE="$MODEL_ECR_DIGEST_REFERENCE"
export VELASERVE_ENDPOINT="$OPENAI_CHAT_COMPLETIONS_URL"

export VELASERVE_VELASERVE_IMAGE="$VELASERVE_ECR_DIGEST_REFERENCE"
export VELASERVE_EPP_IMAGE="$EPP_ECR_TAGGED_DIGEST_REFERENCE"
export VELASERVE_EPP_SELECTOR="llm-d-router-gateway=velaserve-epp"
export VELASERVE_EPP_CONTAINER_NAME="epp"
export VELASERVE_CONDITION_CONTROLLER_SELECTOR="app.kubernetes.io/name=velaserve-condition-controller"
export VELASERVE_CONDITION_CONTROLLER_CONTAINER_NAME="condition-controller"
export VELASERVE_CONTROLLER_REVISION="$(git rev-parse HEAD)"
export VELASERVE_CONDITION_CONTROLLER_ENDPOINT="http://127.0.0.1:18082/v1/conditions/apply"
export VELASERVE_PREREGISTRATION_PATH="$PWD/research/preregistration/z0-v1.yaml"
export VELASERVE_PREREGISTRATION_SHA256="$(shasum -a 256 "$VELASERVE_PREREGISTRATION_PATH" | awk '{print $1}')"
export VELASERVE_ORACLE_CALIBRATION="$REAL_CALIBRATION_PATH"
export VELASERVE_ORACLE_CALIBRATION_SHA256="$(shasum -a 256 "$VELASERVE_ORACLE_CALIBRATION" | awk '{print $1}')"

export VELASERVE_ACTIVE_ARM="arm-b-load-aware-p2p"
export VELASERVE_EPP_REPLICAS="1"
export VELASERVE_Z0_PHASE="z0-b"
export VELASERVE_PREFIX_SOURCE_COUNT="0"
export VELASERVE_ARTIFACT_ROOT="$PWD/benchmarks/raw/aws-z0-arm-b-run-01"
```

The sample EPP selector is the exact label rendered by a release named `velaserve`; verify it with `kubectl get pods --show-labels` after installation. The model workload/container names are operator-owned and must match the actual objects. Mirror the model image into the same account/region ECR because preflight verifies all three declared image digests there.

Do not put AWS access keys in these variables, a `.tfvars` file, Git, or benchmark artifacts. Use the normal AWS credential chain and short-lived credentials.

## 7. Preflight and run

```bash
./hack/cloud-preflight.sh
./hack/cloud-run-z0.sh
```

`cloud-run-z0.sh` repeats preflight. A nonzero `VELASERVE_GROUP_LIMIT` is only a plumbing smoke and intentionally produces an incomplete bundle. Use the complete frozen matrix for gate evidence. Run each arm/EPP/phase combination into a new artifact root; never merge or overwrite run roots.

Preflight writes `.tools/cloud-preflight-binding.json`; the run copies it into the new bundle before traffic and hashes it in the artifact ledger. It binds the checked-out commit, exact model/EPP/controller images and pod UIDs, zero restart counts, model revision, active arm, EPP count, homogeneous GPU nodes, transport, and preregistration/calibration hashes. The gate compiler rejects bundles whose invariant deployment bindings differ.

## 8. Normalize routing records and collect

Export the raw pinned-observer EPP log and normalized Envoy access records. `cloud-collect.sh` extracts only exact `VELASERVE_EPP_RECORD` lines from the raw EPP log, normalizes them, and refuses malformed matching records. Preserve raw originals alongside the operator's private run notes. Then set:

For Z0-C, the model-runtime adapter must also emit one measured acquisition event per child. The repository does not guess transfers from cache candidates. Each JSONL record uses `schema_version: velaserve.p2p-transfer/v1`, the exact run/group/request IDs, `acquisition` (`local`, `recompute`, or `p2p`), and `observed_at`. A P2P event additionally requires `chosen_source`, positive `transfer_bytes`, `transfer_started_at`, and `transfer_completed_at`; measured `source_nic_bytes_per_second` and `source_cpu_tier_utilization` are optional. For example:

```json
{"schema_version":"velaserve.p2p-transfer/v1","run_id":"z0-...","group_id":"...","request_id":"...","acquisition":"p2p","chosen_source":{"id":"model-0","model":"Qwen/Qwen3-8B"},"transfer_bytes":2097152,"transfer_started_at":"2026-08-26T08:00:00Z","transfer_completed_at":"2026-08-26T08:00:00.050Z","observed_at":"2026-08-26T08:00:00.050Z"}
```

`source-pressure-compile` performs the one-to-one join and derives candidate-source count and last-sibling TTFT from the condition-attested groups, so the environment adapter cannot supply those gate inputs itself.

```bash
kubectl --namespace velaserve-z0 logs deployment/velaserve-epp -c epp >"$RAW_EPP_LOG"
export VELASERVE_EPP_LOG_PATH="$RAW_EPP_LOG"
export VELASERVE_ENVOY_RECORDS_PATH="$NORMALIZED_ENVOY_JSONL"
export VELASERVE_P2P_TRANSFER_PATH="$RAW_MODEL_RUNTIME_TRANSFER_JSONL"
export VELASERVE_METRICS_URL="$PROMETHEUS_EXPORT_URL"
./hack/cloud-collect.sh
```

The raw model-runtime transfer input is mandatory for Z0-C and must come from the measured P2P path; omit it for Z0-B. The metrics input is optional. Collection compiles and ledgers the raw transfer/source-pressure pair, ingests placements, recomputes placement cells from raw groups and the hashed real-GPU oracle calibration, snapshots non-secret Kubernetes objects/logs/environment, verifies every ledger entry, refuses an occupied S3 run prefix, uploads to `s3://BUCKET/runs/RUN_ID`, and verifies the remote ledger object.

Review/redact logs before publishing the S3 bundle outside the account. Successful collection prints:

```bash
export VELASERVE_ARTIFACTS_COLLECTED=true
```

## 9. Compile and sign the evidence-derived gate

Run the compiler against the retained local bundles after collection. It independently verifies each ledger, preflight binding, condition receipt, matrix, raw-to-derived placement result, and preregistration copy. It never accepts a caller-selected branch or completeness flag.

Start with the complete Z0-B bundle:

```bash
go run ./cmd/velaserve-gate compile \
  --bundles "$Z0_B_ARTIFACT_ROOT" \
  --preregistration research/preregistration/z0-v1.yaml \
  --output-dir "$NEW_GATE_DIRECTORY" \
  --id "$DECISION_ID"
```

If placement passes, the compiler creates the gate directory and rejects any Z0-C input. If placement does not pass, it fails closed until you run three complete Z0-C bundles with `VELASERVE_PREFIX_SOURCE_COUNT=1`, `2`, and `4`, each into a fresh artifact root. `cloud-run-z0.sh` freezes those profiles to the same `distributed-warm` cache cell. Collect each bundle with its measured P2P telemetry and compiler-derived `source-pressure.jsonl`, then compile all four roots:

```bash
go run ./cmd/velaserve-gate compile \
  --bundles "$Z0_B_ARTIFACT_ROOT,$Z0_C_1_ROOT,$Z0_C_2_ROOT,$Z0_C_4_ROOT" \
  --preregistration research/preregistration/z0-v1.yaml \
  --output-dir "$NEW_GATE_DIRECTORY" \
  --id "$DECISION_ID"
```

Keep the private key outside the repository. These commands bind signing and verification to the compiler's copied preregistration, aggregate bundle ledger, and gate evidence:

```bash
umask 077
go run ./cmd/velaserve-gate keygen --private "$PRIVATE_KEY_PATH" --public "$PUBLIC_KEY_PATH"
go run ./cmd/velaserve-gate sign \
  --decision "$NEW_GATE_DIRECTORY/decision.json" \
  --evidence "$NEW_GATE_DIRECTORY/gate-evidence.jsonl" \
  --preregistration "$NEW_GATE_DIRECTORY/preregistration.yaml" \
  --ledger "$NEW_GATE_DIRECTORY/bundle-ledger.jsonl" \
  --private "$PRIVATE_KEY_PATH" \
  --output "$NEW_GATE_DIRECTORY/signed-decision.json"
go run ./cmd/velaserve-gate verify \
  --signed "$NEW_GATE_DIRECTORY/signed-decision.json" \
  --evidence "$NEW_GATE_DIRECTORY/gate-evidence.jsonl" \
  --preregistration "$NEW_GATE_DIRECTORY/preregistration.yaml" \
  --ledger "$NEW_GATE_DIRECTORY/bundle-ledger.jsonl" \
  --public "$PUBLIC_KEY_PATH"
```

Only a verified signed decision authorizes a new follow-up implementation plan for its exact branch. It does not change this repository automatically.

## 10. Guarded workload cleanup

After independently checking the remote bundle, set the exact confirmation shown by the manifest:

```bash
export VELASERVE_ARTIFACTS_COLLECTED=true
RUN_ID="$(jq -r .run_id "$VELASERVE_ARTIFACT_ROOT/manifest.json")"
export VELASERVE_CONFIRM_CLEANUP="$VELASERVE_CLUSTER_NAME/$RUN_ID"
./hack/cloud-cleanup.sh
```

This removes only the exact VelaServe Helm releases and labeled jobs. Set `VELASERVE_DELETE_GPU_NODEPOOL=true` only when the exact `velaserve-gpu-z0` NodePool/EC2NodeClass should also be removed. EKS, VPC, IAM, ECR, and versioned S3 remain.

## 11. Infrastructure teardown

After all runs, inspect S3, ECR, state, and the Terraform destroy plan separately. The bucket has `force_destroy = false`, so retained evidence prevents an implicit wipe. The operator may remove or archive evidence and execute a reviewed destroy outside repository automation. Record final cost and retained artifact locations in the performance report.
