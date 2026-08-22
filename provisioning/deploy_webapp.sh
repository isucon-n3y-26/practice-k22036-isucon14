#!/usr/bin/env bash
# ==============================================================================
# ISUCON14 webapp (Go) デプロイスクリプト
#
# 【概要】
# ローカルの webapp/go をソースの正として、指定した競技者EC2の
# /home/isucon/webapp/go に rsync で反映し、リモートでビルドして
# isuride-go サービスを再起動します。
#
# 【使用方法】
#   ./provisioning/deploy_webapp.sh <contestant-01|contestant-02|contestant-03>
# ==============================================================================

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TERRAFORM_DIR="${ROOT_DIR}/provisioning/terraform"
SSH_PRIVATE_KEY_PATH="${SSH_PRIVATE_KEY_PATH:-${TERRAFORM_DIR}/.keys/isucon14_ed25519}"
REMOTE_WEBAPP_GO_DIR="/home/isucon/webapp/go"

TARGET="${1:-}"

if [ -z "${TARGET}" ]; then
  echo "Usage: $0 <contestant-01|contestant-02|contestant-03>" >&2
  exit 1
fi

# ------------------------------------------------------------------------------
# 1. 前提コマンド・SSH鍵の確認
# ------------------------------------------------------------------------------
for command_name in rsync ssh terraform jq; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "Required command is not installed: ${command_name}" >&2
    exit 1
  fi
done

if [ ! -f "${SSH_PRIVATE_KEY_PATH}" ]; then
  echo "SSH private key not found: ${SSH_PRIVATE_KEY_PATH}" >&2
  echo "Run ./provisioning/setup.sh first, or set SSH_PRIVATE_KEY_PATH." >&2
  exit 1
fi

SSH_OPTS=(
  -i "${SSH_PRIVATE_KEY_PATH}"
  -o StrictHostKeyChecking=no
  -o UserKnownHostsFile=/dev/null
)

# ------------------------------------------------------------------------------
# 2. Terraform Output から対象EC2のPublic IPを取得
# ------------------------------------------------------------------------------
PUBLIC_IPS_JSON="$(terraform -chdir="${TERRAFORM_DIR}" output -json contestant_public_ips)"
PUBLIC_IP="$(echo "${PUBLIC_IPS_JSON}" | jq -r --arg target "${TARGET}" '.[$target] // empty')"

if [ -z "${PUBLIC_IP}" ]; then
  echo "Unknown target: ${TARGET}" >&2
  echo "Available targets:" >&2
  echo "${PUBLIC_IPS_JSON}" | jq -r 'keys[]' >&2
  exit 1
fi

# ------------------------------------------------------------------------------
# 3. webapp/go をリモートに同期（isucon ユーザー所有のまま反映するため
#    リモート側のrsyncをsudoで実行する）
# ------------------------------------------------------------------------------
printf '\n==> Syncing webapp/go to %s (%s)\n' "${TARGET}" "${PUBLIC_IP}"
rsync -az --delete \
  -e "ssh ${SSH_OPTS[*]}" \
  --rsync-path="sudo -u isucon rsync" \
  "${ROOT_DIR}/webapp/go/" "ubuntu@${PUBLIC_IP}:${REMOTE_WEBAPP_GO_DIR}/"

# ------------------------------------------------------------------------------
# 4. リモートでビルドし、isuride-go を再起動
# ------------------------------------------------------------------------------
printf '\n==> Building and restarting isuride-go\n'
ssh "${SSH_OPTS[@]}" "ubuntu@${PUBLIC_IP}" bash -s <<REMOTE_SCRIPT
set -Eeuo pipefail
sudo -u isucon /home/isucon/local/golang/bin/go build -C "${REMOTE_WEBAPP_GO_DIR}" -o "${REMOTE_WEBAPP_GO_DIR}/isuride" -ldflags "-s -w"
sudo systemctl restart isuride-go
sudo systemctl --no-pager --full status isuride-go | head -n 5
REMOTE_SCRIPT

printf '\nDeployed webapp/go to %s.\n\n' "${TARGET}"
