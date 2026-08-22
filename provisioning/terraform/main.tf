locals {
  # AWS リージョン & プロジェクト設定
  aws_region        = "ap-northeast-1"
  project_name      = "isucon14-practice"
  availability_zone = null

  # ネットワーク設定
  vpc_cidr           = "10.14.0.0/16"
  public_subnet_cidr = "10.14.1.0/24"

  # 競技者 EC2 インスタンス設定
  contestant_count         = 3
  contestant_instance_type = "c6a.large"
  contestant_volume_size   = 20

  # ECS Fargate ベンチマーカー設定
  benchmarker_cpu                = 8192
  benchmarker_memory             = 16384
  benchmarker_image_tag          = "latest"
  benchmark_load_timeout         = 60
  benchmarker_log_retention_days = 7

  common_tags = {
    Environment = "practice"
  }
}

module "network" {
  source = "./modules/network"

  project_name        = local.project_name
  availability_zone   = local.availability_zone
  vpc_cidr            = local.vpc_cidr
  public_subnet_cidr  = local.public_subnet_cidr
  allowed_ssh_cidrs   = var.allowed_ssh_cidrs
  allowed_https_cidrs = var.allowed_https_cidrs
  common_tags         = local.common_tags
}

module "contestant" {
  source = "./modules/contestant"

  project_name      = local.project_name
  instance_count    = local.contestant_count
  instance_type     = local.contestant_instance_type
  volume_size       = local.contestant_volume_size
  subnet_id         = module.network.public_subnet_id
  security_group_id = module.network.contestant_security_group_id
  key_name          = var.key_name
  ssh_public_key    = var.ssh_public_key
  common_tags       = local.common_tags
}

module "benchmarker" {
  source = "./modules/benchmarker"

  project_name       = local.project_name
  aws_region         = local.aws_region
  target_private_ips = module.contestant.private_ips
  cpu                = local.benchmarker_cpu
  memory             = local.benchmarker_memory
  image_tag          = local.benchmarker_image_tag
  load_timeout       = local.benchmark_load_timeout
  log_retention_days = local.benchmarker_log_retention_days
  common_tags        = local.common_tags
}

