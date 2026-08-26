# Security policy

## Supported scope

The current Stage-1 code is a research and benchmark harness, not a production inference control plane. Security fixes are accepted for the latest `main` branch. No released version currently carries a production-support promise.

## Report privately

Do not publish a vulnerability, credential, private endpoint, prompt, model artifact, or AWS account detail in a public issue. Use GitHub's private vulnerability reporting for `JDinSeattle/velaserve` when available. Include the affected commit, minimal reproduction, impact, and whether the issue can expose artifact contents or cause unintended cloud/Kubernetes mutation.

## Safety properties

- Repository scripts never run `terraform apply` or `terraform destroy`.
- Cloud preflight binds execution to an exact AWS account, region, cluster, kubectl context, immutable model revision, and digest-addressed images.
- Artifact paths reject traversal and symlinks; the append-only ledger verifies size and SHA-256 for every recorded file.
- Benchmark evidence excludes prompt text by design.
- Containers run as non-root with dropped capabilities and a read-only root filesystem where the chart owns the pod.
- Cleanup requires verified S3 evidence plus an exact typed confirmation and does not delete Terraform-managed resources.

Operators remain responsible for IAM boundaries, network policy, image provenance, model licenses, data classification, log redaction, AWS budget alarms, and reviewing every Terraform plan before apply.
