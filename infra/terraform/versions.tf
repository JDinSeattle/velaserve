terraform {
  required_version = "= 1.15.8"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "= 6.60.0"
    }
    helm = {
      source  = "hashicorp/helm"
      version = "= 3.2.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "= 3.2.1"
    }
    time = {
      source  = "hashicorp/time"
      version = "= 0.13.1"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "= 4.2.1"
    }
  }
}
