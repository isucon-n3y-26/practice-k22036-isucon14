resource "aws_cloudwatch_log_group" "benchmarker" {
  name              = "/ecs/${var.project_name}/benchmarker"
  retention_in_days = var.log_retention_days

  tags = var.common_tags
}
