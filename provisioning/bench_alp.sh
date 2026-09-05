#!/usr/bin/env bash
# ==============================================================================
# ベンチマーク実行 & alp プロファイリングスクリプト
#
# 【概要】
# 1. 競技者EC2の /var/log/nginx/access.log をリセット（truncate）
# 2. Fargate でベンチマークを実行（run_benchmark.sh を呼び出し）
# 3. ベンチ終了後にEC2上で alp によるアクセスログ集計を表示します
#
# 【使用方法】
#   ./provisioning/bench_alp.sh <contestant-01|contestant-02|contestant-03>
#
# 【前提】
#   - EC2上に alp がインストールされていること（ALP_BIN でパス指定可）
#     例: ssh <host> 'curl -sL https://github.com/tkuchiki/alp/releases/latest/download/alp_linux_amd64.tar.gz | tar zx && sudo install alp /usr/local/bin/'
#   - nginxのログ形式はJSONであること（webapp/nginx/conf.d/alp-log.conf を
#     deploy_webapp.sh でデプロイ済みなら適用済み）
# ==============================================================================

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TERRAFORM_DIR="${ROOT_DIR}/provisioning/terraform"
SSH_PRIVATE_KEY_PATH="${SSH_PRIVATE_KEY_PATH:-${TERRAFORM_DIR}/.keys/isucon14_ed25519}"
ACCESS_LOG="/var/log/nginx/access.json.log"
ALP_BIN="${ALP_BIN:-/usr/local/bin/alp}"

TARGET="${1:-}"

if [ -z "${TARGET}" ]; then
  echo "Usage: $0 <contestant-01|contestant-02|contestant-03>" >&2
  exit 1
fi

# ------------------------------------------------------------------------------
# 1. 前提コマンド・SSH鍵の確認
# ------------------------------------------------------------------------------
for command_name in ssh terraform jq; do
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

PUBLIC_IP="$(terraform -chdir="${TERRAFORM_DIR}" output -json contestant_public_ips \
  | jq -r --arg target "${TARGET}" '.[$target] // empty')"

if [ -z "${PUBLIC_IP}" ]; then
  echo "Unknown target: ${TARGET}" >&2
  exit 1
fi

remote() {
  ssh "${SSH_OPTS[@]}" "ubuntu@${PUBLIC_IP}" "$@"
}

# ------------------------------------------------------------------------------
# 2. アクセスログのリセット
# ------------------------------------------------------------------------------
printf '\n==> Truncating %s on %s\n' "${ACCESS_LOG}" "${TARGET}"
remote "sudo truncate -s 0 ${ACCESS_LOG} && sudo chown syslog:adm ${ACCESS_LOG}"

# ------------------------------------------------------------------------------
# 3. ベンチマーク実行（Fargate）
#    失敗してもアクセスログは記録されているため、alp 集計を継続する
# ------------------------------------------------------------------------------
set +e
"${ROOT_DIR}/provisioning/run_benchmark.sh" "${TARGET}"
BENCH_EXIT_CODE=$?
set -e

# ------------------------------------------------------------------------------
# 4. alp による集計
# ------------------------------------------------------------------------------
printf '\n==> Profiling access log with alp\n'
remote "command -v '${ALP_BIN}' >/dev/null || { echo 'alp not found: ${ALP_BIN}' >&2; exit 1; } && \
  sudo cat ${ACCESS_LOG} | '${ALP_BIN}' json --sort avg --output count,method,uri,min,max,sum,avg,p99 --format md -m '/api/chair/rides/[^/]+/status,/api/app/rides/[^/]+/evaluation,/assets/.*,/images/.*'"

if [ "${BENCH_EXIT_CODE}" != "0" ]; then
  printf '\nDone (benchmark failed with exit code %s, but profiling completed).\n' "${BENCH_EXIT_CODE}"
  exit "${BENCH_EXIT_CODE}"
fi

printf '\nDone.\n'
