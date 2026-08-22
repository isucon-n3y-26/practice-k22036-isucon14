resource "aws_ecs_task_definition" "benchmarker" {
  count = length(var.target_private_ips)

  family                   = format("%s-benchmarker-%02d", var.project_name, count.index + 1)
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = tostring(var.cpu)
  memory                   = tostring(var.memory)
  execution_role_arn       = aws_iam_role.ecs_task_execution.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "X86_64"
  }

  container_definitions = jsonencode([{
    name      = "benchmarker"
    image     = "${aws_ecr_repository.benchmarker.repository_url}:${var.image_tag}"
    essential = true
    cpu       = var.cpu
    memory    = var.memory
    command = [
      "/app/bench",
      "run",
      "--target",
      "https://isuride.xiv.isucon.net",
      "--addr",
      "${var.target_private_ips[count.index]}:443",
      "--load-timeout",
      tostring(var.load_timeout),
      "--fail-on-error"
    ]
    portMappings = [{
      name          = "payment"
      containerPort = 12345
      hostPort      = 12345
      protocol      = "tcp"
      appProtocol   = "http"
    }]
    linuxParameters = {
      initProcessEnabled = true
    }
    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.benchmarker.name
        awslogs-region        = var.aws_region
        awslogs-stream-prefix = format("contestant-%02d", count.index + 1)
      }
    }
  }])

  tags = merge(var.common_tags, {
    Role   = "benchmarker"
    Target = format("contestant-%02d", count.index + 1)
  })
}
