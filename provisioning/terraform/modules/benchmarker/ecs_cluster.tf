resource "aws_ecs_cluster" "benchmarker" {
  name = "${var.project_name}-benchmarker"

  setting {
    name  = "containerInsights"
    value = "disabled"
  }

  tags = var.common_tags
}
