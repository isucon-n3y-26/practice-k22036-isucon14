output "ecr_repository_url" {
  value = aws_ecr_repository.benchmarker.repository_url
}

output "ecs_cluster_arn" {
  value = aws_ecs_cluster.benchmarker.arn
}

output "task_definition_arns" {
  value = aws_ecs_task_definition.benchmarker[*].arn
}

output "log_group_name" {
  value = aws_cloudwatch_log_group.benchmarker.name
}
