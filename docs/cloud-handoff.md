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

Build VelaServe from the verified commit and llm-d-router from commit `ab723b898f8598ab6631e9848a4cf28accd9b9ea`. Push both to the Terraform-created immutable ECR repositories. Resolve and record full references of the form:

```text
ACCOUNT.dkr.ecr.REGION.amazonaws.com/REPOSITORY@sha256:DIGEST
```

Tags alone are not accepted by preflight. Cloud preflight queries ECR to ensure each digest exists in the declared account and region.

## 4. Deploy the homogeneous model fleet

The operator deploys vLLM because model credentials, storage, image, and license are environment-specific. The real fleet contract is:

- one immutable model revision and tokenizer across all pods;
- six to eight ready pods on one approved instance type;
- one GPU per pod for the documented first shape;
- label `app.kubernetes.io/name=velaserve-model`;
- Service `velaserve-model` in namespace `velaserve-z0`, port 8000;
- OpenAI-compatible `/v1/chat/completions`, `/metrics`, and the llm-d KV event/P2P integration required by the pinned router;
- consistent cache block/connector settings and declared `tcp` or `efa` transport;
- no autoscaling, restart, image update, or mixed hardware during an evidence cell.

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
  --set-string images.upstreamEPP.tag="git-ab723b898f8598ab6631e9848a4cf28accd9b9ea@sha256:$EPP_IMAGE_DIGEST"

kubectl --namespace velaserve-z0 get configmap velaserve-upstream-router-values \
  -o 'jsonpath={.data.values\.yaml}' >.tools/aws-router-values.yaml
.tools/bin/helm upgrade --install velaserve \
  .tools/upstream/llm-d-router/config/charts/llm-d-router-standalone \
  --namespace velaserve-z0 \
  -f .tools/aws-router-values.yaml
kubectl apply -f deploy/gateway/httproute.yaml
```

Inspect both renders before installing. `aws-z0-values.yaml` disables simfleet and points only at the declared real model service and label. Install the pinned Envoy Gateway and Gateway API Inference Extension releases before applying the route, using the versions in `docs/upstream-version-matrix.md`.

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
export VELASERVE_GPU_INSTANCE_TYPES="g6e.xlarge"
export VELASERVE_P2P_TRANSPORT="tcp"

export VELASERVE_MODEL_SELECTOR="app.kubernetes.io/name=velaserve-model"
export VELASERVE_MODEL_ID="Qwen/Qwen3-8B"
export VELASERVE_MODEL_REVISION="$IMMUTABLE_MODEL_REVISION"
export VELASERVE_ENDPOINT="$OPENAI_CHAT_COMPLETIONS_URL"

export VELASERVE_VELASERVE_IMAGE="$VELASERVE_ECR_DIGEST_REFERENCE"
export VELASERVE_EPP_IMAGE="$EPP_ECR_DIGEST_REFERENCE"
export VELASERVE_PREREGISTRATION_PATH="$PWD/research/preregistration/z0-v1.yaml"
export VELASERVE_PREREGISTRATION_SHA256="$(shasum -a 256 "$VELASERVE_PREREGISTRATION_PATH" | awk '{print $1}')"
export VELASERVE_ORACLE_CALIBRATION="$REAL_CALIBRATION_PATH"
export VELASERVE_ORACLE_CALIBRATION_SHA256="$(shasum -a 256 "$VELASERVE_ORACLE_CALIBRATION" | awk '{print $1}')"

export VELASERVE_ACTIVE_ARM="arm-b-load-aware-p2p"
export VELASERVE_EPP_REPLICAS="1"
export VELASERVE_Z0_PHASE="z0-a"
export VELASERVE_ARTIFACT_ROOT="$PWD/benchmarks/raw/aws-z0-arm-b-run-01"
```

Do not put AWS access keys in these variables, a `.tfvars` file, Git, or benchmark artifacts. Use the normal AWS credential chain and short-lived credentials.

## 7. Preflight and run

```bash
./hack/cloud-preflight.sh
./hack/cloud-run-z0.sh
```

`cloud-run-z0.sh` repeats preflight. A nonzero `VELASERVE_GROUP_LIMIT` is only a plumbing smoke and intentionally produces an incomplete bundle. Use the complete frozen matrix for gate evidence. Run each arm/EPP/phase combination into a new artifact root; never merge or overwrite run roots.

## 8. Normalize routing records and collect

Export EPP and Envoy records as normalized JSONL accepted by `placement-recorder`. Preserve their raw originals alongside the operator's private run notes. Then set:

```bash
export VELASERVE_EPP_RECORDS_PATH="$NORMALIZED_EPP_JSONL"
export VELASERVE_ENVOY_RECORDS_PATH="$NORMALIZED_ENVOY_JSONL"
export VELASERVE_SOURCE_PRESSURE_PATH="$NORMALIZED_SOURCE_PRESSURE_JSONL"
export VELASERVE_METRICS_URL="$PROMETHEUS_EXPORT_URL"
./hack/cloud-collect.sh
```

The source-pressure and metrics inputs are optional; omit their environment variables when they were not measured. Collection ingests placements, runs the hashed real-GPU oracle calibration, snapshots non-secret Kubernetes objects/logs/environment, verifies every ledger entry, refuses an occupied S3 run prefix, uploads to `s3://BUCKET/runs/RUN_ID`, and verifies the remote ledger object.

Review/redact logs before publishing the S3 bundle outside the account. Successful collection prints:

```bash
export VELASERVE_ARTIFACTS_COLLECTED=true
```

## 9. Guarded workload cleanup

After independently checking the remote bundle, set the exact confirmation shown by the manifest:

```bash
export VELASERVE_ARTIFACTS_COLLECTED=true
RUN_ID="$(jq -r .run_id "$VELASERVE_ARTIFACT_ROOT/manifest.json")"
export VELASERVE_CONFIRM_CLEANUP="$VELASERVE_CLUSTER_NAME/$RUN_ID"
./hack/cloud-cleanup.sh
```

This removes only the exact VelaServe Helm releases and labeled jobs. Set `VELASERVE_DELETE_GPU_NODEPOOL=true` only when the exact `velaserve-gpu-z0` NodePool/EC2NodeClass should also be removed. EKS, VPC, IAM, ECR, and versioned S3 remain.

## 10. Infrastructure teardown

After all runs, inspect S3, ECR, state, and the Terraform destroy plan separately. The bucket has `force_destroy = false`, so retained evidence prevents an implicit wipe. The operator may remove or archive evidence and execute a reviewed destroy outside repository automation. Record final cost and retained artifact locations in the performance report.
