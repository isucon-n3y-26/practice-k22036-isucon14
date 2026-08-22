output "aws_region" {
  description = "AWS region used by this environment."
  value       = local.aws_region
}

output "contestant_instances" {
  description = "Connection information for contestant instances."
  value       = module.contestant.instances
}

output "contestant_public_ips" {
  description = "Public IP addresses used to generate the Ansible inventory."
  value       = module.contestant.public_ips
}

output "ssm_commands" {
  description = "Commands for connecting to contestant instances through SSM Session Manager."
  value = [
    for instance_id in module.contestant.instance_ids :
    join(" ", compact([
      "aws ssm start-session",
      var.aws_profile != null ? "--profile ${var.aws_profile}" : "",
      "--region ${local.aws_region}",
      "--target ${instance_id}",
    ]))
  ]
}

output "benchmarker_ecr_repository_url" {
  description = "ECR repository URL for the benchmarker image."
  value       = module.benchmarker.ecr_repository_url
}

output "benchmarker_ecr_registry" {
  description = "ECR registry hostname used by docker login."
  value       = split("/", module.benchmarker.ecr_repository_url)[0]
}

output "benchmarker_image_uri" {
  description = "Full benchmarker image URI expected by the Fargate task definitions."
  value       = "${module.benchmarker.ecr_repository_url}:${local.benchmarker_image_tag}"
}

output "benchmarker_ecs_cluster_arn" {
  description = "ECS cluster ARN used to run benchmark tasks."
  value       = module.benchmarker.ecs_cluster_arn
}

output "benchmarker_log_group_name" {
  description = "CloudWatch Logs group name that benchmark tasks write to."
  value       = module.benchmarker.log_group_name
}

output "benchmark_commands" {
  description = "Commands that start one Fargate benchmark task for each contestant."
  value = {
    for index, task_arn in module.benchmarker.task_definition_arns : format("contestant-%02d", index + 1) => join(" ", compact([
      "aws ecs run-task",
      var.aws_profile != null ? "--profile ${var.aws_profile}" : "",
      "--region ${local.aws_region}",
      "--cluster ${module.benchmarker.ecs_cluster_arn}",
      "--task-definition ${task_arn}",
      "--launch-type FARGATE",
      "--platform-version LATEST",
      "--count 1",
      "--network-configuration '${jsonencode({ awsvpcConfiguration = { subnets = [module.network.public_subnet_id], securityGroups = [module.network.benchmarker_security_group_id], assignPublicIp = "ENABLED" } })}'"
    ]))
  }
}
