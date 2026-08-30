#!/usr/bin/env bash
# ==============================================================================
# ISUCON14 webapp (Go) & nginx 設定 & スキーマデプロイスクリプト
#
# 【概要】
# ローカルの webapp/go をソースの正として、指定した競技者EC2の
# /home/isucon/webapp/go に rsync で反映し、リモートでビルドして
# isuride-go サービスを再起動します。
#
# あわせて webapp/nginx/conf.d/ をEC2の /etc/nginx/conf.d/ に同期し、
# nginx -t の後に nginx を reload します。
#
# webapp/sql/ も同期したうえで /api/initialize を実行し、DBスキーマと
# 初期データを再構築します（1-schema.sql の変更が反映される）。
# 再初期化をスキップしたい場合は SKIP_DB_INIT=1 を設定してください。
#
# 【使用方法】
#   ./provisioning/deploy_webapp.sh <contestant-01|contestant-02|contestant-03>
#   SKIP_DB_INIT=1 ./provisioning/deploy_webapp.sh contestant-01
# ==============================================================================

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TERRAFORM_DIR="${ROOT_DIR}/provisioning/terraform"
SSH_PRIVATE_KEY_PATH="${SSH_PRIVATE_KEY_PATH:-${TERRAFORM_DIR}/.keys/isucon14_ed25519}"
REMOTE_WEBAPP_GO_DIR="/home/isucon/webapp/go"
REMOTE_WEBAPP_SQL_DIR="/home/isucon/webapp/sql"
REMOTE_NGINX_CONF_D_DIR="/etc/nginx/conf.d"
REMOTE_ENV_SH_PATH="/home/isucon/env.sh"

TARGET="${1:-}"
SKIP_DB_INIT="${SKIP_DB_INIT:-0}"

# デバッグ用設定（webapp/sql/profiling.enabled ファイルの有無で切り替え）
# 有効化: touch webapp/sql/profiling.enabled && ./provisioning/deploy_webapp.sh <target>
# 無効化: rm webapp/sql/profiling.enabled && ./provisioning/deploy_webapp.sh <target>
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [ -f "${SCRIPT_DIR}/webapp/sql/profiling.enabled" ]; then
  export ISUCON_LOG_LEVEL="debug"
  export ISUCON_LOG_FILE="/home/isucon/isuride-go.log"
  export ISUCON_LOG_ERROR_FILE="/home/isucon/isuride-go-error.log"
  printf '\n==> App debug logging ENABLED (level=debug, file output)\n'
else
  export ISUCON_LOG_LEVEL="info"
  export ISUCON_LOG_FILE=""
  export ISUCON_LOG_ERROR_FILE=""
  printf '\n==> App debug logging DISABLED (level=info, stdout/stderr)\n'
fi

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

remote() {
  ssh "${SSH_OPTS[@]}" "ubuntu@${PUBLIC_IP}" "$@"
}

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
# 4. webapp/nginx/conf.d を /etc/nginx/conf.d に同期（root所有のため
#    リモート側のrsyncをsudoで実行する）
# ------------------------------------------------------------------------------
printf '\n==> Syncing nginx conf.d to %s (%s)\n' "${TARGET}" "${PUBLIC_IP}"
rsync -az --delete \
  -e "ssh ${SSH_OPTS[*]}" \
  --rsync-path="sudo rsync" \
  "${ROOT_DIR}/webapp/nginx/conf.d/" "ubuntu@${PUBLIC_IP}:${REMOTE_NGINX_CONF_D_DIR}/"

# ------------------------------------------------------------------------------
# 4.5 sites-available/isuride.conf を同期(upstream keepalive 適用済みの全文)
#     単一ファイル同期とする(--delete でのディレクトリ同期は
#     isuride-php.conf 等を消すため行わない)
# ------------------------------------------------------------------------------
printf '\n==> Syncing nginx sites-available/isuride.conf to %s (%s)\n' "${TARGET}" "${PUBLIC_IP}"
rsync -az \
  -e "ssh ${SSH_OPTS[*]}" \
  --rsync-path="sudo rsync" \
  "${ROOT_DIR}/webapp/nginx/sites-available/isuride.conf" \
  "ubuntu@${PUBLIC_IP}:/etc/nginx/sites-available/isuride.conf"

# ------------------------------------------------------------------------------
# 5. webapp/sql をリモートに同期（isucon ユーザー所有のまま反映するため
#    リモート側のrsyncをsudoで実行する）
# ------------------------------------------------------------------------------
printf '\n==> Syncing webapp/sql to %s (%s)\n' "${TARGET}" "${PUBLIC_IP}"
rsync -az --delete \
  -e "ssh ${SSH_OPTS[*]}" \
  --rsync-path="sudo -u isucon rsync" \
  "${ROOT_DIR}/webapp/sql/" "ubuntu@${PUBLIC_IP}:${REMOTE_WEBAPP_SQL_DIR}/"

# ------------------------------------------------------------------------------
# 5.5 env.sh をリモートに同期（isucon ユーザー所有のまま反映するため
#     リモート側のrsyncをsudoで実行する）
# ------------------------------------------------------------------------------
printf '\n==> Syncing env.sh to %s (%s)\n' "${TARGET}" "${PUBLIC_IP}"
# ログ設定を env.sh に反映（既存行を置換、なければ追加）
update_env_var() {
  local file="$1"
  local var="$2"
  local value="$3"
  if grep -q "^${var}=" "${file}"; then
    # macOS sed requires -i '' for in-place edit without backup
    if [[ "$(uname)" == "Darwin" ]]; then
      sed -i '' "s|^${var}=.*|${var}=\"${value}\"|" "${file}"
    else
      sed -i "s|^${var}=.*|${var}=\"${value}\"|" "${file}"
    fi
  else
    echo "${var}=\"${value}\"" >> "${file}"
  fi
}
update_env_var "${ROOT_DIR}/env.sh" "ISUCON_LOG_LEVEL" "${ISUCON_LOG_LEVEL}"
update_env_var "${ROOT_DIR}/env.sh" "ISUCON_LOG_FILE" "${ISUCON_LOG_FILE}"
update_env_var "${ROOT_DIR}/env.sh" "ISUCON_LOG_ERROR_FILE" "${ISUCON_LOG_ERROR_FILE}"
rsync -az \
  -e "ssh ${SSH_OPTS[*]}" \
  --rsync-path="sudo -u isucon rsync" \
  "${ROOT_DIR}/env.sh" "ubuntu@${PUBLIC_IP}:${REMOTE_ENV_SH_PATH}"

# ------------------------------------------------------------------------------
# 6. リモートでビルドし、isuride-go を再起動（isuride-matcher はGo内部常駐のため停止・無効化）
# ------------------------------------------------------------------------------
printf '\n==> Cleaning old logs\n'
remote bash -s <<'REMOTE_SCRIPT'
set -Eeuo pipefail
for logfile in /home/isucon/isuride-go.log /home/isucon/isuride-go-error.log; do
  if [ -f "$logfile" ]; then
    sudo -u isucon rm -f "$logfile"
    echo "Removed $logfile"
  fi
done
REMOTE_SCRIPT

printf '\n==> Building and restarting services\n'
remote bash -s <<REMOTE_SCRIPT
set -Eeuo pipefail
sudo -u isucon /home/isucon/local/golang/bin/go build -C "${REMOTE_WEBAPP_GO_DIR}" -o "${REMOTE_WEBAPP_GO_DIR}/isuride" -ldflags "-s -w"
sudo systemctl restart isuride-go
sudo systemctl stop isuride-matcher || true
sudo systemctl disable isuride-matcher || true
sudo nginx -t && sudo systemctl reload nginx
sudo systemctl --no-pager --full status isuride-go | head -n 5
REMOTE_SCRIPT


# ------------------------------------------------------------------------------
# 7. /api/initialize を実行し DB スキーマと初期データを再構築
#    （webapp/sql/1-schema.sql 等の変更が反映される）
# ------------------------------------------------------------------------------
if [ "${SKIP_DB_INIT}" = "1" ]; then
  printf '\n==> Skipping DB initialization (SKIP_DB_INIT=1)\n\n'
  printf 'Deployed webapp/go, nginx conf.d and webapp/sql to %s.\n\n' "${TARGET}"
  exit 0
fi

printf '\n==> Re-initializing DB schema via /api/initialize\n'
remote "curl -sf -X POST http://127.0.0.1:8080/api/initialize -H 'Content-Type: application/json' -d '{\"payment_server\":\"http://localhost:12345\"}'"

printf '\nDeployed webapp/go, nginx conf.d and webapp/sql to %s.\n\n' "${TARGET}"
