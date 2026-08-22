resource "aws_ecr_lifecycle_policy" "benchmarker" {
  repository = aws_ecr_repository.benchmarker.name

  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "Keep the latest five benchmarker images"
      selection = {
        tagStatus   = "any"
        countType   = "imageCountMoreThan"
        countNumber = 5
      }
      action = {
        type = "expire"
      }
    }]
  })
}
