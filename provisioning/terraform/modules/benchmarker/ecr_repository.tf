resource "aws_ecr_repository" "benchmarker" {
  name         = "${var.project_name}-benchmarker"
  force_delete = true

  image_scanning_configuration {
    scan_on_push = true
  }

  tags = var.common_tags
}
