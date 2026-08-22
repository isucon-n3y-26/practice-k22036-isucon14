#!/usr/bin/env bash
# ==============================================================================
# ISUCON14 AWS 過去問環境 一括クリーンアップスクリプト
#
# 【概要】
# 本スクリプトは、setup.sh で作成した AWS リソース（Terraform管理）の破棄と、
# ローカルに生成された一時ファイル・Dockerイメージの削除を一括で行います。
#
# 【使用方法】
#   ./provisioning/cleanup.sh          # 確認プロンプトを表示して破棄
#   ./provisioning/cleanup.sh -y       # 確認をスキップして即時破棄
# ==============================================================================

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TERRAFORM_DIR="${ROOT_DIR}/provisioning/terraform"
KEY_DIR="${TERRAFORM_DIR}/.keys"
GENERATED_DIR="${TERRAFORM_DIR}/generated"
LOCAL_BENCHMARKER_IMAGE="isucon14-benchmarker:setup"

AUTO_APPROVE=false

# 引数解析
for arg in "$@"; do
  case "$arg" in
    -y|--yes|-auto-approve|--auto-approve)
      AUTO_APPROVE=true
      ;;
    -h|--help)
      echo "Usage: $0 [-y|--yes]"
      echo "  -y, --yes: Auto-approve Terraform destroy without interactive confirmation."
      exit 0
      ;;
  esac
done

if ! command -v terraform >/dev/null 2>&1; then
  echo "Error: terraform is not installed." >&2
  exit 1
fi

echo "================================================================="
echo " ISUCON14 AWS Practice Environment Cleanup"
echo "================================================================="
echo ""
echo "This will destroy all AWS resources created by Terraform in:"
echo "  ${TERRAFORM_DIR}"
echo ""

# ------------------------------------------------------------------------------
# 1. Terraform による AWS リソースの削除
# ------------------------------------------------------------------------------
if [ -d "${TERRAFORM_DIR}/.terraform" ] || [ -f "${TERRAFORM_DIR}/terraform.tfstate" ]; then
  printf '==> Destroying AWS resources with Terraform\n'
  if [ "${AUTO_APPROVE}" = true ]; then
    terraform -chdir="${TERRAFORM_DIR}" destroy -auto-approve
  else
    terraform -chdir="${TERRAFORM_DIR}" destroy
  fi
else
  echo "==> No Terraform state found. Skipping AWS resource destruction."
fi

# ------------------------------------------------------------------------------
# 2. ローカルの一時ファイル・生成物のクリーンアップ
# ------------------------------------------------------------------------------
printf '\n==> Cleaning up local generated files and keys\n'

if [ -d "${GENERATED_DIR}" ]; then
  rm -rf "${GENERATED_DIR}"
  echo "Removed: ${GENERATED_DIR}"
fi

if [ -d "${KEY_DIR}" ]; then
  rm -rf "${KEY_DIR}"
  echo "Removed: ${KEY_DIR}"
fi

AUTO_TFVARS_FILE="${TERRAFORM_DIR}/setup.auto.tfvars.json"
if [ -f "${AUTO_TFVARS_FILE}" ]; then
  rm -f "${AUTO_TFVARS_FILE}"
  echo "Removed: ${AUTO_TFVARS_FILE}"
fi

# Ansible配布用のビルド成果物
if [ -f "${ROOT_DIR}/provisioning/ansible/roles/webapp/files/webapp.tar.gz" ]; then
  rm -f "${ROOT_DIR}/provisioning/ansible/roles/webapp/files/webapp.tar.gz"
  echo "Removed: webapp.tar.gz"
fi

if [ -f "${ROOT_DIR}/provisioning/ansible/roles/bench/files/bench_linux_amd64" ]; then
  rm -f "${ROOT_DIR}/provisioning/ansible/roles/bench/files/bench_linux_amd64"
  echo "Removed: bench_linux_amd64"
fi

# ------------------------------------------------------------------------------
# 3. ローカル Docker イメージの削除（Dockerが起動している場合）
# ------------------------------------------------------------------------------
if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
  if docker image inspect "${LOCAL_BENCHMARKER_IMAGE}" >/dev/null 2>&1; then
    printf '\n==> Removing local benchmarker Docker image\n'
    docker rmi "${LOCAL_BENCHMARKER_IMAGE}" || true
    echo "Removed Docker image: ${LOCAL_BENCHMARKER_IMAGE}"
  fi
fi

printf '\nCleanup completed successfully. All AWS resources and temporary files have been removed.\n\n'
