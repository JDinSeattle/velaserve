# Failure analysis

## Stage-1 failure behavior

| Failure | Detection | Consequence |
|---|---|---|
| Invalid or oversized SSE event | Streaming parser bound/validation | Child and group are retained as failed; no transparent retry |
| Missing `[DONE]` or content | Terminal stream validation | Run is incomplete and cannot support a positive gate |
| Duplicate/malformed request identity | Evidence validator/recorder | Record is rejected or written unmatched |
| Missing Envoy/EPP correlation | `unmatched.jsonl` and ingest counts | Artifact verification may pass, but evidence completeness does not |
| Modified artifact after recording | SHA-256 and byte-count verification | Bundle verification fails |
| Duplicate artifact path | Append-only ledger guard | Collection stops instead of overwriting provenance |
| Changed preregistration/calibration | Git and SHA-256 preflight | Cloud run refuses to start |
| Wrong AWS account, region, cluster, or kubectl context | Exact preflight checks | No benchmark request is sent |
| Floating/missing image identity | Required ECR `@sha256:` references | Cloud run refuses to start |
| Fewer than six or heterogeneous model replicas | Pod readiness/node-label preflight | Real-GPU evidence run refuses to start |
| Insufficient GPU quota | Service Quotas check | Real-GPU evidence run refuses to start |
| EFA requested without node resource | Node allocatable check | Preflight fails; transport cannot be relabeled TCP |
| Partial benchmark limit or interruption | Run-completion accounting | Raw bundle is retained and marked incomplete |
| Existing S3 run prefix | Destination presence check | Upload refuses to merge with prior evidence |
| Cleanup before verified upload | Local marker, remote ledger, typed confirmation | Cleanup refuses to run |

## Upstream and routing failures

The Stage-1 InferencePool uses fail-open behavior. VelaServe adds no online picker, shared state, or failure mode to that path. A local two-EPP test observes dispersion but does not claim cross-replica consistency. Endpoint loss, router restart, and control-plane timing may invalidate a benchmark cell; they are retained and excluded only under the frozen rules.

## Oracle model risk

The offline oracle is only as valid as its endpoint snapshot and calibration. It chooses among local hit, peer pull, and recompute using measured throughput and service-time inputs, then serializes predicted work per target. It does not model every vLLM queue, CUDA kernel, cache eviction, NIC interaction, or scheduler race. The report must show predicted results separately from measured outcomes, hash the calibration, and require a real-GPU confirmation before a gate decision.

## Artifact and privacy limits

The ledger detects mutation; it does not encrypt local files or prevent an authorized actor from replacing an entire unsigned bundle. S3 encryption/versioning and a signed final decision strengthen provenance after upload. Operators must still redact model logs and metrics that can contain prompts, tenant identifiers, endpoints, or account details before publishing artifacts. The Kubernetes snapshot deliberately omits Secrets, but application logs remain operator-controlled.

## Cloud operational limits

Terraform is validated locally but has not been planned or applied against the user's AWS account. GPU availability, quota, AMI compatibility, model fit, EFA behavior, regional price, and vLLM/llm-d runtime compatibility remain cloud validation tasks. The repository does not deploy model weights or a vLLM StatefulSet/Deployment because those inputs depend on the operator's model license, registry, storage, and GPU choice.

Repository cleanup deletes only exact Helm releases/jobs and, when separately opted in, the exact GPU NodePool/EC2NodeClass. Terraform-managed VPC, EKS, IAM, ECR, and S3 remain until the operator reviews and executes an independent destroy plan.
