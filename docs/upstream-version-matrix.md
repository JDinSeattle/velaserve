# Upstream version matrix

`versions.lock.yaml` is the machine-readable source of truth. Git dependencies use full commit SHAs so moving branches cannot change the baseline.

| Component | Frozen revision/version | Use in Stage 1 |
|---|---|---|
| llm-d Router | `ab723b898f8598ab6631e9848a4cf28accd9b9ea` | EPP source build and routing profiles |
| llm-d | `3243fcf1191348b55c7811267a98117f8b7a6910` | Baseline manifests/reference |
| llm-d KV Cache | `8cf43067afb7fc9fefafc1b64de063c769f2c90f` | P2P/KV integration reference |
| vLLM | `b1fbbc2ade51e3826bc92e4733c9c692ee21d42d` | Real model-engine baseline |
| Go | `1.26.6` | Build and CI toolchain |
| Helm | `3.21.4` | Chart lint/render/install |
| Kind | `0.32.0` | Local Kubernetes harness |
| Terraform | `1.15.8` | AWS configuration validation/plan |
| Envoy Gateway | `v1.9.0` | Local gateway controller |
| Gateway API Inference Extension | `v1.5.0` | Local InferencePool/route CRDs |
| Kind node | Kubernetes `v1.35.0`, pinned image digest | Local cluster image |
| Karpenter chart | `1.11.3` | AWS scale-to-zero GPU capacity |
| Terraform AWS provider | `6.60.0` | AWS resources/data sources |
| Terraform Kubernetes provider | `3.2.1` | Namespace, service account, Pod Identity association inputs |
| Terraform Helm provider | `3.2.0` | Pinned Karpenter install |
| Terraform EKS module | `21.24.2` | EKS and managed CPU node group |
| Terraform VPC module | `6.6.1` | Dedicated experiment network |

`hack/fetch-upstream.sh` clones each Git repository, checks out the exact commit, verifies `HEAD`, and refuses a dirty checkout. `hack/build-pinned-epp.sh` exports that exact tree into a temporary directory, checks and applies `deploy/upstream-patches/llm-d-router-stage1-observer.patch`, and builds an EPP whose only VelaServe behavior is emitting request-scoped scheduling evidence when valid Vela fan-out headers are present. The patch does not change candidates, scores, selection, headers, or routing. `hack/bootstrap-tools.sh` downloads platform archives and verifies publisher checksums before installation. Terraform's dependency lock file records provider hashes across supported platforms.

Image tags in the local harness are paired with locally built exact source. A real-GPU run must additionally use ECR `@sha256:` references and record them in preflight/artifacts. The model revision must be a 40-to-64-character immutable content identifier, not a branch or friendly tag.

Updating any row creates a new baseline: update the lock, preregistration version, fetch/render tests, cloud handoff, and evidence version as appropriate. Do not compare a new upstream stack with prior results under the same experiment identity.
