#!/usr/bin/env bash
# ==============================================================================
# ISUCON14 ベンチマーク実行 & ログダウンロードスクリプト
#
# 【概要】
# 指定した競技者（contestant-01 等）に対して Fargate ベンチマークタスクを
# 起動し、タスクの終了を待機したうえで、そのタスク専用の CloudWatch Logs
# ストリームをローカルファイルにダウンロードします。
#
# 【使用方法】
#   ./provisioning/run_benchmark.sh <contestant-01|contestant-02|contestant-03> [log_dir]
#
#   log_dir を省略した場合は provisioning/terraform/generated/logs に保存します。
# ==============================================================================

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TERRAFORM_DIR="${ROOT_DIR}/provisioning/terraform"
DEFAULT_LOG_DIR="${TERRAFORM_DIR}/generated/logs"
SSH_PRIVATE_KEY_PATH="${SSH_PRIVATE_KEY_PATH:-${TERRAFORM_DIR}/.keys/isucon14_ed25519}"
SLOW_QUERY_LOG_PATH="/var/log/mysql/slow.log"

TARGET="${1:-}"
LOG_DIR="${2:-${DEFAULT_LOG_DIR}}"

if [ -z "${TARGET}" ]; then
  echo "Usage: $0 <contestant-01|contestant-02|contestant-03> [log_dir]" >&2
  exit 1
fi

# ------------------------------------------------------------------------------
# 1. 前提コマンドの確認
# ------------------------------------------------------------------------------
for command_name in aws terraform jq; do
  if ! command -v "${command_name}" >/dev/null 2>&1; then
    echo "Required command is not installed: ${command_name}" >&2
    exit 1
  fi
done

# AWS プロファイルの自動検出（setup.sh と同様のロジック）
if [ -z "${AWS_PROFILE:-}" ]; then
  TFVARS_FILE="${TERRAFORM_DIR}/terraform.tfvars"
  if [ -f "${TFVARS_FILE}" ]; then
    _detected_profile="$(grep -E '^\s*aws_profile\s*=' "${TFVARS_FILE}" \
      | head -1 \
      | sed 's/.*=[[:space:]]*"\(.*\)".*/\1/' 2>/dev/null || true)"
    if [ -n "${_detected_profile}" ]; then
      export AWS_PROFILE="${_detected_profile}"
    fi
    unset _detected_profile
  fi
fi

# ------------------------------------------------------------------------------
# 2. Terraform Output から実行に必要な情報を取得
# ------------------------------------------------------------------------------
BENCHMARK_COMMANDS_JSON="$(terraform -chdir="${TERRAFORM_DIR}" output -json benchmark_commands)"
RUN_TASK_CMD="$(echo "${BENCHMARK_COMMANDS_JSON}" | jq -r --arg target "${TARGET}" '.[$target] // empty')"

if [ -z "${RUN_TASK_CMD}" ]; then
  echo "Unknown target: ${TARGET}" >&2
  echo "Available targets:" >&2
  echo "${BENCHMARK_COMMANDS_JSON}" | jq -r 'keys[]' >&2
  exit 1
fi

CLUSTER_ARN="$(terraform -chdir="${TERRAFORM_DIR}" output -raw benchmarker_ecs_cluster_arn)"
LOG_GROUP_NAME="$(terraform -chdir="${TERRAFORM_DIR}" output -raw benchmarker_log_group_name)"

# ------------------------------------------------------------------------------
# 3. 競技者EC2へのSSH接続準備（スロークエリログの確認用）
# ------------------------------------------------------------------------------
PUBLIC_IP="$(terraform -chdir="${TERRAFORM_DIR}" output -json contestant_public_ips \
  | jq -r --arg target "${TARGET}" '.[$target] // empty')"

remote() {
  ssh -i "${SSH_PRIVATE_KEY_PATH}" \
      -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
      "ubuntu@${PUBLIC_IP}" "$@"
}

# ------------------------------------------------------------------------------
# ------------------------------------------------------------------------------
# 4. Fargate ベンチマークタスクの起動
#    （今回の実行分だけを分析できるよう、起動前にスロークエリログをリセット）
# ------------------------------------------------------------------------------
printf '\n==> Truncating slow query log on %s\n' "${TARGET}"
remote "sudo truncate -s 0 ${SLOW_QUERY_LOG_PATH}"

printf '\n==> Starting benchmark task for %s\n' "${TARGET}"
RUN_TASK_OUTPUT="$(sh -c "${RUN_TASK_CMD}")"

FAILURE_COUNT="$(echo "${RUN_TASK_OUTPUT}" | jq -r '.failures // [] | length')"
if [ "${FAILURE_COUNT}" != "0" ]; then
  echo "Failed to start the benchmark task:" >&2
  echo "${RUN_TASK_OUTPUT}" | jq '.failures' >&2
  exit 1
fi

TASK_ARN="$(echo "${RUN_TASK_OUTPUT}" | jq -r '.tasks[0].taskArn')"
TASK_ID="${TASK_ARN##*/}"
echo "Task ARN: ${TASK_ARN}"

# ------------------------------------------------------------------------------
# 5. タスク終了までの待機
# ------------------------------------------------------------------------------
printf '\n==> Waiting for the benchmark task to finish (this can take a few minutes)\n'
aws ecs wait tasks-stopped --cluster "${CLUSTER_ARN}" --tasks "${TASK_ARN}"

EXIT_CODE="$(aws ecs describe-tasks --cluster "${CLUSTER_ARN}" --tasks "${TASK_ARN}" \
  --query 'tasks[0].containers[0].exitCode' --output text)"

# ------------------------------------------------------------------------------
# 6. CloudWatch Logs からこのタスク専用のログをダウンロード
#    ログストリーム名は awslogs-stream-prefix (=TARGET) / コンテナ名 / タスクID
# ------------------------------------------------------------------------------
mkdir -p "${LOG_DIR}"
LOG_STREAM_NAME="${TARGET}/benchmarker/${TASK_ID}"
LOG_FILE="${LOG_DIR}/${TARGET}-${TASK_ID}.log"

printf '\n==> Downloading logs from CloudWatch Logs\n'
aws logs tail "${LOG_GROUP_NAME}" --log-stream-names "${LOG_STREAM_NAME}" --since 1d >"${LOG_FILE}"

printf '\nBenchmark finished. Container exit code: %s\n' "${EXIT_CODE}"
printf 'Log saved to: %s\n\n' "${LOG_FILE}"

# ------------------------------------------------------------------------------
# 7. スロークエリログの存在確認
# ------------------------------------------------------------------------------
set +e
SLOW_LOG_INFO="$(remote "sudo test -s ${SLOW_QUERY_LOG_PATH} && sudo wc -l ${SLOW_QUERY_LOG_PATH}")"
set -e

if [ -n "${SLOW_LOG_INFO}" ]; then
  printf 'Slow query log found: %s:%s (%s)\n' "${PUBLIC_IP}" "${SLOW_QUERY_LOG_PATH}" "${SLOW_LOG_INFO}"
  printf 'View with: ssh -i %s ubuntu@%s "sudo tail -100 %s"\n\n' \
    "${SSH_PRIVATE_KEY_PATH}" "${PUBLIC_IP}" "${SLOW_QUERY_LOG_PATH}"
fi

if [ "${EXIT_CODE}" != "0" ]; then
  exit 1
fi
