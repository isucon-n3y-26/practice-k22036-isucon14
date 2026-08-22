# Terraform modules

- `network`: VPC、Public Subnet、Internet Gateway、Route Table、Security Group
- `contestant`: 競技者EC2とSSM接続用IAM
- `benchmarker`: ベンチマーカー用ECR、ECS Fargate Task Definition、IAM、CloudWatch Logs

ルートモジュールがこれらを接続し、利用者向けの変数とoutputsを提供します。
