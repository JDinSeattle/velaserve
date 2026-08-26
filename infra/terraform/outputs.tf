output "aws_account_id" {
  description = "AWS account observed while planning."
  value       = data.aws_caller_identity.current.account_id
}

output "cluster_name" {
  description = "Exact EKS cluster name consumed by every handoff guard."
  value       = module.eks.cluster_name
}

output "cluster_endpoint" {
  description = "EKS API endpoint."
  value       = module.eks.cluster_endpoint
}

output "configure_kubectl_command" {
  description = "Explicit command for configuring the operator's kubeconfig."
  value       = "aws eks update-kubeconfig --name ${module.eks.cluster_name} --region ${var.aws_region}"
}

output "artifact_bucket" {
  description = "Versioned, encrypted experiment artifact bucket."
  value       = aws_s3_bucket.artifacts.bucket
}

output "artifact_writer_role_arn" {
  description = "Pod Identity role scoped to the artifact bucket runs prefix."
  value       = aws_iam_role.artifact_writer.arn
}

output "velaserve_ecr_repository_url" {
  description = "Immutable VelaServe image repository."
  value       = aws_ecr_repository.velaserve.repository_url
}

output "llm_d_router_ecr_repository_url" {
  description = "Immutable pinned llm-d-router image repository."
  value       = aws_ecr_repository.llm_d_router.repository_url
}

output "karpenter_gpu_ec2_node_class_yaml" {
  description = "Apply only after reviewing the exact AMI, role, subnets, and two security groups."
  value = yamlencode({
    apiVersion = "karpenter.k8s.aws/v1"
    kind       = "EC2NodeClass"
    metadata = {
      name = "velaserve-gpu-z0"
    }
    spec = {
      amiFamily = "AL2023"
      role      = module.karpenter.node_iam_role_name
      amiSelectorTerms = [{
        id = var.gpu_ami_id
      }]
      subnetSelectorTerms = [{
        tags = {
          "karpenter.sh/discovery" = var.cluster_name
        }
      }]
      securityGroupSelectorTerms = [
        { id = module.eks.node_security_group_id },
        { id = aws_security_group.gpu_p2p.id },
      ]
      tags = merge(local.common_tags, {
        "velaserve.dev/role" = "gpu-z0"
      })
    }
  })
}

output "karpenter_gpu_node_pool_yaml" {
  description = "Scale-to-zero GPU NodePool; GPU instances launch only when matching pods are pending."
  value = yamlencode({
    apiVersion = "karpenter.sh/v1"
    kind       = "NodePool"
    metadata = {
      name = "velaserve-gpu-z0"
    }
    spec = {
      template = {
        metadata = {
          labels = {
            "velaserve.dev/role"           = "gpu-z0"
            "velaserve.dev/evidence-scope" = "real-gpu-pending"
          }
        }
        spec = {
          nodeClassRef = {
            group = "karpenter.k8s.aws"
            kind  = "EC2NodeClass"
            name  = "velaserve-gpu-z0"
          }
          requirements = [
            {
              key      = "kubernetes.io/arch"
              operator = "In"
              values   = ["amd64"]
            },
            {
              key      = "karpenter.sh/capacity-type"
              operator = "In"
              values   = [var.gpu_capacity_type]
            },
            {
              key      = "node.kubernetes.io/instance-type"
              operator = "In"
              values   = var.gpu_instance_types
            },
          ]
          taints = [{
            key    = "nvidia.com/gpu"
            value  = "true"
            effect = "NoSchedule"
          }]
          expireAfter = "720h"
        }
      }
      disruption = {
        consolidationPolicy = "WhenEmptyOrUnderutilized"
        consolidateAfter    = "5m"
      }
      limits = {
        cpu              = "512"
        "nvidia.com/gpu" = tostring(var.gpu_maximum_replicas)
      }
    }
  })
}

output "gpu_benchmark_contract" {
  description = "Operator-visible scale-to-zero and real-run replica contract."
  value = {
    desired_before_run = var.gpu_desired_size
    minimum_real_run   = var.gpu_minimum_benchmark_replicas
    maximum_real_run   = var.gpu_maximum_replicas
    instance_types     = var.gpu_instance_types
    ami_id             = var.gpu_ami_id
  }
}
