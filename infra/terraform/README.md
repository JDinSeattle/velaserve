# AWS Z0 infrastructure handoff

This directory is an operator-reviewed Terraform handoff. VelaServe does not run `terraform apply` or `terraform destroy` from any repository script. The configuration creates a dedicated VPC, EKS control plane, two-node CPU system group, Karpenter IAM/queue/controller prerequisites, immutable ECR repositories, an encrypted and versioned S3 artifact bucket, Pod Identity for artifact writes, and a GPU peer security group.

GPU capacity is scale-to-zero. The `gpu_desired_size` default is `0`; Karpenter launches nodes only after the operator applies the rendered `EC2NodeClass` and `NodePool` and schedules matching GPU pods. A real evidence run requires six to eight homogeneous replicas. The documented first choice is `g6e.xlarge` (one L40S), but the operator must confirm regional availability, EC2 service quota, model fit, AMI, price, and transport before planning.

## Review and plan

1. Copy `terraform.tfvars.example` to an untracked `terraform.tfvars` and replace the sample account-derived bucket, operator CIDR, and GPU AMI with exact values.
2. Configure a remote state backend if this is more than a disposable personal experiment. No backend is silently created here.
3. Run `../../.tools/bin/terraform init -backend=false` for syntax validation, then initialize the reviewed backend for the actual plan.
4. Run `../../.tools/bin/terraform plan -out=velaserve-z0.tfplan` and review every resource, IAM statement, region, quota-dependent GPU choice, NAT cost, and public endpoint CIDR.
5. The operator—not VelaServe automation—decides whether to run `terraform apply velaserve-z0.tfplan`.

After apply, configure kubectl with the `configure_kubectl_command` output. Review and apply the scale-to-zero Karpenter resources explicitly:

```bash
terraform output -raw karpenter_gpu_ec2_node_class_yaml | kubectl apply -f -
terraform output -raw karpenter_gpu_node_pool_yaml | kubectl apply -f -
```

Push both VelaServe and the frozen llm-d-router commit image to the output ECR repositories, record their `@sha256:` references, deploy the six-to-eight homogeneous model-server replicas, and then use `hack/cloud-preflight.sh`, `hack/cloud-run-z0.sh`, and `hack/cloud-collect.sh` from the repository root.

## Cleanup boundary

`hack/cloud-cleanup.sh` removes only exact VelaServe Kubernetes releases/jobs after artifact collection and a typed confirmation. It does not delete the EKS cluster, VPC, ECR repositories, or versioned S3 evidence. After independently confirming the S3 bundle and reviewing the Terraform destroy plan, the operator may run Terraform destroy manually. The artifact bucket has `force_destroy = false`, so retained evidence prevents accidental deletion.
