#!/usr/bin/env bash
# ==============================================================================
# ISUCON14 AWS 過去問環境 一括セットアップスクリプト
#
# 【概要】
# 本スクリプトは、ISUCON14の練習環境（EC2 3台 + ECS Fargateベンチマーカー）を
# 1コマンドで構築・プロビジョニングする自動化スクリプトです。
#
# 【処理フロー】
# 1. 必要な前提コマンドおよびDockerデーモンの稼働チェック
# 2. EC2接続用SSH鍵（ed25519）の自動生成
# 3. 実行元パブリックIPv4アドレスの取得（セキュリティグループ制限用）
# 4. フロントエンド・ベンチマーカー・webappアーカイブのビルド (Ansible用)
# 5. Fargate用ベンチマーカーDockerイメージのビルド (linux/amd64)
# 6. Terraform による AWS インフラ構築 (VPC, ECR, ECS, EC2 3台)
# 7. Terraform 出力をもとに Ansible インベントリ (inventory.ini) を生成
# 8. ECR へのベンチマーカーDockerイメージのログイン & プッシュ
# 9. EC2 の SSH接続可能状態および cloud-init 完了の待機
# 10. Ansible による競技者環境のプロビジョニング (Go版 isuride, MySQL, Nginx等)
# 11. セットアップ完了とベンチマーク実行コマンドの表示
# ==============================================================================

set -Eeuo pipefail

# ------------------------------------------------------------------------------
# 1. ディレクトリパスおよび定数の設定
# ------------------------------------------------------------------------------
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TERRAFORM_DIR="${ROOT_DIR}/provisioning/terraform"
ANSIBLE_DIR="${ROOT_DIR}/provisioning/ansible"
KEY_DIR="${TERRAFORM_DIR}/.keys"
GENERATED_DIR="${TERRAFORM_DIR}/generated"
SSH_PRIVATE_KEY_PATH="${SSH_PRIVATE_KEY_PATH:-${KEY_DIR}/isucon14_ed25519}"
LOCAL_BENCHMARKER_IMAGE="isucon14-benchmarker:setup"

# ------------------------------------------------------------------------------
# 2. 必要な前提コマンドおよびDockerデーモンのチェック
# ------------------------------------------------------------------------------
required_commands=(
  ansible
  ansible-playbook
  aws
  curl
  docker
  go
  jq
  make
  node
  pnpm
  ssh-keygen
  task
  terraform
)

for command_name in "${required_commands[@]}"; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "Required command is not installed: ${command_name}" >&2
    exit 1
  fi
done

# Docker デーモンの稼働確認（イメージビルドおよび ECR push に必要）
if ! docker info >/dev/null 2>&1; then
  echo "Docker daemon is not running. Start Docker Desktop and retry." >&2
  exit 1
fi

# ------------------------------------------------------------------------------
# 2.5. AWS プロファイルの自動検出
#       terraform.tfvars に aws_profile が設定されている場合、AWS CLI にも
#       同じプロファイルを適用する（環境変数 AWS_PROFILE が未設定の場合のみ）
# ------------------------------------------------------------------------------
if [ -z "${AWS_PROFILE:-}" ]; then
  TFVARS_FILE="${TERRAFORM_DIR}/terraform.tfvars"
  if [ -f "${TFVARS_FILE}" ]; then
    _detected_profile="$(grep -E '^\s*aws_profile\s*=' "${TFVARS_FILE}" \
      | head -1 \
      | sed 's/.*=[[:space:]]*"\(.*\)".*/\1/' 2>/dev/null || true)"
    if [ -n "${_detected_profile}" ]; then
      export AWS_PROFILE="${_detected_profile}"
      printf '  Using AWS profile from terraform.tfvars: %s\n' "${AWS_PROFILE}"
    fi
    unset _detected_profile
  fi
fi

# ------------------------------------------------------------------------------
# 3. SSH 鍵ペアの生成（未作成の場合のみ）
# ------------------------------------------------------------------------------
mkdir -p "${KEY_DIR}" "${GENERATED_DIR}"
chmod 0700 "${KEY_DIR}"

if [ ! -f "${SSH_PRIVATE_KEY_PATH}" ]; then
  ssh-keygen -q -t ed25519 -N "" -f "${SSH_PRIVATE_KEY_PATH}"
fi
chmod 0600 "${SSH_PRIVATE_KEY_PATH}"

if [ ! -f "${SSH_PRIVATE_KEY_PATH}.pub" ]; then
  ssh-keygen -y -f "${SSH_PRIVATE_KEY_PATH}" >"${SSH_PRIVATE_KEY_PATH}.pub"
fi

# ------------------------------------------------------------------------------
# 4. 実行元のパブリック IPv4 アドレスを取得し、Terraform 変数に設定
#    （EC2のSSH接続元を自身のIPアドレスのみに制限します）
# ------------------------------------------------------------------------------
PUBLIC_IP="${SSH_ALLOWED_IP:-$(curl --fail --silent https://checkip.amazonaws.com)}"
PUBLIC_IP="$(printf '%s' "${PUBLIC_IP}" | tr -d '[:space:]')"
if [[ ! "${PUBLIC_IP}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "Could not determine a valid public IPv4 address: ${PUBLIC_IP}" >&2
  exit 1
fi

SSH_PUBLIC_KEY="$(cat "${SSH_PRIVATE_KEY_PATH}.pub")"

# ssh_public_key / allowed_ssh_cidrs を *.auto.tfvars.json に書き出します。
# 環境変数 (TF_VAR_*) だけで渡すと、このスクリプトのシェルプロセスを抜けた後に
# 手動で terraform plan/apply/destroy を実行した際、これらの変数がデフォルト値
# (null / []) に戻り、生成済みキーペアの削除やSSH許可CIDRの削除という意図しない
# 差分が発生してしまうため、ファイルとして永続化します。
AUTO_TFVARS_FILE="${TERRAFORM_DIR}/setup.auto.tfvars.json"
jq -n \
  --arg ssh_public_key "${SSH_PUBLIC_KEY}" \
  --arg cidr "${PUBLIC_IP}/32" \
  '{ssh_public_key: $ssh_public_key, allowed_ssh_cidrs: [$cidr]}' \
  >"${AUTO_TFVARS_FILE}"

# ------------------------------------------------------------------------------
# 5. アプリケーション成果物のビルド（Ansible 配信用）
#    - フロントエンドのビルド (pnpm)
#    - ベンチマーカーバイナリ (linux/amd64)
#    - envcheck バイナリ
#    - webapp.tar.gz の作成
# ------------------------------------------------------------------------------
printf '\n==> Building application artifacts for Ansible\n'
(
  cd "${ANSIBLE_DIR}"
  ./make_latest_files.sh
)

# ------------------------------------------------------------------------------
# 6. Fargate 用ベンチマーカー Docker イメージのビルド (linux/amd64)
# ------------------------------------------------------------------------------
printf '\n==> Building the Fargate benchmarker image\n'
docker buildx build \
  --platform linux/amd64 \
  --file "${ROOT_DIR}/bench/Dockerfile.fargate" \
  --load \
  --tag "${LOCAL_BENCHMARKER_IMAGE}" \
  "${ROOT_DIR}/bench"

# ------------------------------------------------------------------------------
# 7. Terraform による AWS インフラの作成
#    - VPC, Subnet, Route Table, Security Group
#    - ECR リポジトリ
#    - ECS クラスター & Fargate タスク定義
#    - 競技者 EC2 (c6a.large × 3, 公式 Ubuntu 24.04 AMI)
# ------------------------------------------------------------------------------
printf '\n==> Creating AWS resources with Terraform\n'
terraform -chdir="${TERRAFORM_DIR}" init -input=false
terraform -chdir="${TERRAFORM_DIR}" apply -auto-approve "$@"

# Terraform Output から必要な情報を取得
AWS_REGION="$(terraform -chdir="${TERRAFORM_DIR}" output -raw aws_region)"
ECR_REGISTRY="$(terraform -chdir="${TERRAFORM_DIR}" output -raw benchmarker_ecr_registry)"
BENCHMARKER_IMAGE_URI="$(terraform -chdir="${TERRAFORM_DIR}" output -raw benchmarker_image_uri)"
INVENTORY_PATH="${GENERATED_DIR}/inventory.ini"

# ------------------------------------------------------------------------------
# 8. Ansible 用インベントリファイル (inventory.ini) の自動生成
# ------------------------------------------------------------------------------
{
  echo "[application]"
  terraform -chdir="${TERRAFORM_DIR}" output -json contestant_public_ips \
    | jq -r --arg key "${SSH_PRIVATE_KEY_PATH}" \
      'to_entries[] | "\(.key) ansible_host=\(.value) ansible_user=ubuntu ansible_ssh_private_key_file=\"\($key)\" ansible_python_interpreter=/usr/bin/python3"'
} >"${INVENTORY_PATH}"

# ------------------------------------------------------------------------------
# 9. ECR へベンチマーカー Docker イメージをプッシュ
# ------------------------------------------------------------------------------
printf '\n==> Pushing the benchmarker image to ECR\n'
aws ecr get-login-password --region "${AWS_REGION}" \
  | docker login --username AWS --password-stdin "${ECR_REGISTRY}"
docker tag "${LOCAL_BENCHMARKER_IMAGE}" "${BENCHMARKER_IMAGE_URI}"
docker push "${BENCHMARKER_IMAGE_URI}"

# ------------------------------------------------------------------------------
# 10. 競技者 EC2 インスタンスの起動・初期化待機
#     - SSH 接続ができるようになるまで待機 (wait_for_connection)
#     - cloud-init の完了を待機 (cloud-init status --wait)
# ------------------------------------------------------------------------------
printf '\n==> Waiting for contestant instances\n'
export ANSIBLE_HOST_KEY_CHECKING=False
ansible application \
  --inventory "${INVENTORY_PATH}" \
  --module-name wait_for_connection \
  --args "timeout=600"
ansible application \
  --inventory "${INVENTORY_PATH}" \
  --become \
  --module-name command \
  --args "cloud-init status --wait"

# ------------------------------------------------------------------------------
# 11. Ansible による競技者環境のプロビジョニング
#     - システム初期設定 (base, apt)
#     - isucon ユーザー作成
#     - xbuild (Go 1.23.2 のインストール)
#     - MySQL, Nginx のインストール・設定
#     - isuride (Go), isuride-matcher, isuride-payment_mock の起動
# ------------------------------------------------------------------------------
printf '\n==> Provisioning contestant instances with Ansible\n'
ansible-playbook \
  --inventory "${INVENTORY_PATH}" \
  "${ANSIBLE_DIR}/application.yml"

# ------------------------------------------------------------------------------
# 12. セットアップ完了メッセージおよびベンチマーク実行コマンドの案内
# ------------------------------------------------------------------------------
printf '\nSetup completed successfully.\n\n'
printf 'Run a benchmark (starts the task, waits for it to finish, and downloads its logs) with:\n'
printf '  %q contestant-01\n' "${ROOT_DIR}/provisioning/run_benchmark.sh"
printf '\nOr start the task manually with:\n'
printf '  cd %q\n' "${TERRAFORM_DIR}"
printf '  sh -c "$(terraform output -json benchmark_commands | jq -r '\''.[\"contestant-01\"]'\'')"\n'

