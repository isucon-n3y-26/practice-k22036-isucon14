#!/usr/bin/env bash
# ==============================================================================
# ISUCON14 役割分散デプロイスクリプト
#
# 【構成】(Private IP は実行時に各ホストへSSHで問い合わせて取得。決め打ちなし)
#   contestant-01 (app):  goapp専用。DBは02(MySQL)を参照する
#   contestant-02 (db):   MySQL専用
#   contestant-03 (edge): nginx専用。静的配信+TLS終端+01へのAPI proxy
#   benchの向き先: contestant-03 (../run_benchmark.sh contestant-03)
#
# 【使用方法】
#   ./provisioning/deploy_webapp.sh                       # 全ロールを db→app→edge の順にデプロイ
#   ./provisioning/deploy_webapp.sh <contestant-01|contestant-02|contestant-03>
#   SKIP_DB_INIT=1 ./provisioning/deploy_webapp.sh        # 初期化なしで全ロール更新
#   SKIP_DB_INIT=1 ./provisioning/deploy_webapp.sh contestant-01
#
# 【ロール別動作】
#   app:
#     webapp/go・webapp/sql同期、env.shをtmp展開して同期
#     (ISUCON_DB_HOSTは02の実IPを動的取得。repoのenv.shは書き換えない)、
#     webapp/hosts/contestant-01 同期(isuride-go.service本体。local mysql依存なし)、
#     ビルド・isuride-go再起動、不要サービス停止(mysql/nginx/matcher)。
#     末尾で /api/initialize を実行し02のDBスキーマ・初期データを再構築する。
#     初期化を挟まずappだけ更新する場合は SKIP_DB_INIT=1 を付ける。
#   db:
#     webapp/mysql/conf.d + webapp/hosts/contestant-02 同期後 mysql再起動、
#     不要サービス停止(isuride-go/matcher/nginx)。SKIP_DB_INITの有無は無視される。
#   edge:
#     webapp/nginx/{conf.d,sites-available/isuride.conf,nginx.conf} +
#     webapp/public + webapp/hosts/contestant-03
#     (upstreamを01の実IPに展開)同期後 nginx -t/reload、
#     不要サービス停止(isuride-go/matcher/mysql)。SKIP_DB_INITの有無は無視される。
#
# 【host別設定】
#   webapp/hosts/<target>/ 配下がリモートの / に対応する形で同期される。
#   @APP_PRIVATE_IP@ / @DB_PRIVATE_IP@ はデプロイ時に実IPへ展開される。
# ==============================================================================

set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TERRAFORM_DIR="${ROOT_DIR}/provisioning/terraform"
SSH_PRIVATE_KEY_PATH="${SSH_PRIVATE_KEY_PATH:-${TERRAFORM_DIR}/.keys/isucon14_ed25519}"
REMOTE_WEBAPP_GO_DIR="/home/isucon/webapp/go"
REMOTE_WEBAPP_SQL_DIR="/home/isucon/webapp/sql"
REMOTE_WEBAPP_PUBLIC_DIR="/home/isucon/webapp/public"
REMOTE_NGINX_CONF_D_DIR="/etc/nginx/conf.d"
REMOTE_MYSQL_CONF_D_DIR="/etc/mysql/conf.d"
REMOTE_ENV_SH_PATH="/home/isucon/env.sh"

TARGET="${1:-}"
SKIP_DB_INIT="${SKIP_DB_INIT:-0}"

# 引数なしは全ロールを db→app→edge の順にデプロイする
# (db先行でmysqlを確保し、appのinitialize・edgeのupstream参照を成立させる)
if [ -z "${TARGET}" ]; then
  printf '==> No target specified: deploying all roles (db -> app -> edge)\n'
  "$0" contestant-02
  "$0" contestant-01
  "$0" contestant-03
  printf '\nAll roles deployed.\n'
  exit 0
fi

if [ "${TARGET}" = "-h" ] || [ "${TARGET}" = "--help" ]; then
  sed -n '2,/^# ==/p' "$0" | sed 's/^# \{0,1\}//'
  exit 0
fi

# Private IP は各ホストにSSHで問い合わせて取得する（決め打ちしない）。
# hostname -I の先頭アドレスを採用する。

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

case "${TARGET}" in
  contestant-01) ROLE="app" ;;
  contestant-02) ROLE="db" ;;
  contestant-03) ROLE="edge" ;;
  *)
    echo "Unknown target: ${TARGET}" >&2
    exit 1
    ;;
esac

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

printf '\n==> Target: %s (%s) role=%s\n' "${TARGET}" "${PUBLIC_IP}" "${ROLE}"

# 指定ターゲットのPrivate IPをSSH経由で取得する
private_ip_of() {
  local target_name="$1"
  local public_ip
  public_ip="$(echo "${PUBLIC_IPS_JSON}" | jq -r --arg target "${target_name}" '.[$target] // empty')"
  if [ -z "${public_ip}" ]; then
    echo "Unknown target: ${target_name}" >&2
    exit 1
  fi
  ssh "${SSH_OPTS[@]}" "ubuntu@${public_ip}" "hostname -I | awk '{print \$1}'"
}

# webapp/hosts/<target>/ 配下をリモートの絶対パスに同期する。
# ディレクトリ構成はリモートの / に対応する
# (例: hosts/contestant-02/etc/mysql/... → /etc/mysql/...)。
# @APP_PRIVATE_IP@ / @DB_PRIVATE_IP@ はデプロイ時に実IPへ展開する
# (未展開が残ったらエラー終了)。root所有パスのみ対応。
HOSTS_DIR="${ROOT_DIR}/webapp/hosts"
sync_overlay() {
  local overlay_dir="${HOSTS_DIR}/${TARGET}"
  if [ ! -d "${overlay_dir}" ]; then
    return 0
  fi
  printf '\n==> Syncing host overlay %s\n' "${overlay_dir}"
  local tmpdir
  tmpdir="$(mktemp -d)"
  local src_file rel_path remote_path rendered
  while IFS= read -r src_file; do
    rel_path="${src_file#"${overlay_dir}"/}"
    remote_path="/${rel_path}"
    remote_dir="$(dirname "${remote_path}")"
    if [ "${remote_dir}" != "/" ]; then
      remote "sudo mkdir -p '${remote_dir}'"
    fi
    if grep -q '@[A-Z_]*@' "${src_file}"; then
      rendered="${tmpdir}/rendered.conf"
      sed -e "s|@APP_PRIVATE_IP@|${APP_PRIVATE_IP:-}|g" \
          -e "s|@DB_PRIVATE_IP@|${DB_PRIVATE_IP:-}|g" \
          "${src_file}" > "${rendered}"
      if grep -q '@[A-Z_]*@' "${rendered}"; then
        echo "Unexpanded placeholder remains in ${src_file}" >&2
        rm -rf "${tmpdir}"
        exit 1
      fi
      rsync -az \
        -e "ssh ${SSH_OPTS[*]}" \
        --rsync-path="sudo rsync" \
        "${rendered}" "ubuntu@${PUBLIC_IP}:${remote_path}"
    else
      rsync -az \
        -e "ssh ${SSH_OPTS[*]}" \
        --rsync-path="sudo rsync" \
        "${src_file}" "ubuntu@${PUBLIC_IP}:${remote_path}"
    fi
  done < <(find "${overlay_dir}" -type f)
  rm -rf "${tmpdir}"
}

# ==============================================================================
# ROLE=db: MySQL設定のみ同期して再起動
# ==============================================================================
if [ "${ROLE}" = "db" ]; then
  printf '\n==> Syncing mysql conf.d to %s (%s)\n' "${TARGET}" "${PUBLIC_IP}"
  rsync -az --delete \
    -e "ssh ${SSH_OPTS[*]}" \
    --rsync-path="sudo rsync" \
    "${ROOT_DIR}/webapp/mysql/conf.d/" "ubuntu@${PUBLIC_IP}:${REMOTE_MYSQL_CONF_D_DIR}/"

  sync_overlay

  printf '\n==> Restarting mysql\n'
  remote bash -s <<'REMOTE_SCRIPT'
set -Eeuo pipefail
sudo systemctl restart mysql
for i in $(seq 1 30); do
  if sudo mysqladmin ping -h 127.0.0.1 --silent 2>/dev/null; then
    break
  fi
  sleep 1
done
  sudo mysqladmin ping -h 127.0.0.1 --silent
REMOTE_SCRIPT

  printf '\n==> Stopping unneeded services (appStack only needs mysql)\n'
  remote bash -s <<'REMOTE_SCRIPT'
set -Eeuo pipefail
sudo systemctl stop isuride-go isuride-matcher nginx || true
sudo systemctl disable isuride-go isuride-matcher nginx || true
REMOTE_SCRIPT

  printf '\nMySQL config deployed to %s.\n\n' "${TARGET}"
  exit 0
fi

# ==============================================================================
# ROLE=edge: nginx設定+静的アセットのみ同期し、upstreamを01に向けてreload
# (TLS証明書 /etc/nginx/tls は初回のみ手動コピー。goapp/mysqlは停止のまま)
# ==============================================================================
if [ "${ROLE}" = "edge" ]; then
  printf '\n==> Syncing nginx conf.d to %s (%s)\n' "${TARGET}" "${PUBLIC_IP}"
  rsync -az --delete \
    -e "ssh ${SSH_OPTS[*]}" \
    --rsync-path="sudo rsync" \
    "${ROOT_DIR}/webapp/nginx/conf.d/" "ubuntu@${PUBLIC_IP}:${REMOTE_NGINX_CONF_D_DIR}/"

  printf '\n==> Syncing nginx sites-available/isuride.conf to %s (%s)\n' "${TARGET}" "${PUBLIC_IP}"
  rsync -az \
    -e "ssh ${SSH_OPTS[*]}" \
    --rsync-path="sudo rsync" \
    "${ROOT_DIR}/webapp/nginx/sites-available/isuride.conf" \
    "ubuntu@${PUBLIC_IP}:/etc/nginx/sites-available/isuride.conf"

  printf '\n==> Syncing nginx nginx.conf to %s (%s)\n' "${TARGET}" "${PUBLIC_IP}"
  remote bash -s <<'REMOTE_SCRIPT'
set -Eeuo pipefail
if [ ! -f /etc/nginx/nginx.conf.bak ]; then
  sudo cp -p /etc/nginx/nginx.conf /etc/nginx/nginx.conf.bak
fi
REMOTE_SCRIPT
  rsync -az \
    -e "ssh ${SSH_OPTS[*]}" \
    --rsync-path="sudo rsync" \
    "${ROOT_DIR}/webapp/nginx/nginx.conf" \
    "ubuntu@${PUBLIC_IP}:/etc/nginx/nginx.conf"

  printf '\n==> Syncing public/ static assets to %s (%s)\n' "${TARGET}" "${PUBLIC_IP}"
  rsync -az --delete \
    -e "ssh ${SSH_OPTS[*]}" \
    --rsync-path="sudo -u isucon rsync" \
    "${ROOT_DIR}/webapp/public/" "ubuntu@${PUBLIC_IP}:${REMOTE_WEBAPP_PUBLIC_DIR}/"

  printf '\n==> Rendering host overlay (upstream to app host) and reloading nginx\n'
  APP_PRIVATE_IP="$(private_ip_of contestant-01)"
  printf 'upstream target: %s\n' "${APP_PRIVATE_IP}"
  sync_overlay
  remote bash -s <<'REMOTE_SCRIPT'
set -Eeuo pipefail
grep -E 'server .*8080' /etc/nginx/conf.d/isuride-tuning.conf
sudo nginx -t && sudo systemctl reload nginx
sudo systemctl stop isuride-go isuride-matcher mysql || true
sudo systemctl disable isuride-go isuride-matcher mysql || true
REMOTE_SCRIPT

  printf '\nEdge nginx deployed to %s.\n\n' "${TARGET}"
  exit 0
fi

# ==============================================================================
# ROLE=app: goapp同期・ビルド・再起動 (+env の DB_HOST は02を向ける)
# (01上の mysql/nginx は停止済みのため触らない)
# ==============================================================================
if [ "${ROLE}" != "app" ]; then
  echo "Unknown role: ${ROLE}" >&2
  exit 1
fi

printf '\n==> Syncing webapp/go to %s (%s)\n' "${TARGET}" "${PUBLIC_IP}"
rsync -az --delete \
  -e "ssh ${SSH_OPTS[*]}" \
  --rsync-path="sudo -u isucon rsync" \
  "${ROOT_DIR}/webapp/go/" "ubuntu@${PUBLIC_IP}:${REMOTE_WEBAPP_GO_DIR}/"

printf '\n==> Syncing webapp/sql to %s (%s)\n' "${TARGET}" "${PUBLIC_IP}"
rsync -az --delete \
  -e "ssh ${SSH_OPTS[*]}" \
  --rsync-path="sudo -u isucon rsync" \
  "${ROOT_DIR}/webapp/sql/" "ubuntu@${PUBLIC_IP}:${REMOTE_WEBAPP_SQL_DIR}/"

printf '\n==> Syncing env.sh to %s (%s)\n' "${TARGET}" "${PUBLIC_IP}"
update_env_var() {
  local file="$1"
  local var="$2"
  local value="$3"
  if grep -q "^${var}=" "${file}"; then
    if [[ "$(uname)" == "Darwin" ]]; then
      sed -i '' "s|^${var}=.*|${var}=\"${value}\"|" "${file}"
    else
      sed -i "s|^${var}=.*|${var}=\"${value}\"|" "${file}"
    fi
  else
    echo "${var}=\"${value}\"" >> "${file}"
  fi
}
# repo直下の env.sh は書き換えない。tmpに複製して動値を展開してから送る
# (ISUCON_DB_HOST は実行時に02へ問い合わせた実IPを入れる)
ENV_TMP="$(mktemp)"
cp "${ROOT_DIR}/env.sh" "${ENV_TMP}"
update_env_var "${ENV_TMP}" "ISUCON_DB_HOST" "$(private_ip_of contestant-02)"
update_env_var "${ENV_TMP}" "ISUCON_LOG_LEVEL" "${ISUCON_LOG_LEVEL}"
update_env_var "${ENV_TMP}" "ISUCON_LOG_FILE" "${ISUCON_LOG_FILE}"
update_env_var "${ENV_TMP}" "ISUCON_LOG_ERROR_FILE" "${ISUCON_LOG_ERROR_FILE}"
rsync -az \
  -e "ssh ${SSH_OPTS[*]}" \
  --rsync-path="sudo -u isucon rsync" \
  "${ENV_TMP}" "ubuntu@${PUBLIC_IP}:${REMOTE_ENV_SH_PATH}"
rm -f "${ENV_TMP}"

sync_overlay
# 旧drop-inが残っていれば除去(isuride-go.service 本体置換に一本化したため)
remote "sudo rm -rf /etc/systemd/system/isuride-go.service.d && sudo systemctl daemon-reload"

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

printf '\n==> Building and restarting isuride-go\n'
remote bash -s <<REMOTE_SCRIPT
set -Eeuo pipefail
sudo -u isucon /home/isucon/local/golang/bin/go build -C "${REMOTE_WEBAPP_GO_DIR}" -o "${REMOTE_WEBAPP_GO_DIR}/isuride" -ldflags "-s -w"
sudo systemctl restart isuride-go
sudo systemctl stop isuride-matcher || true
sudo systemctl disable isuride-matcher || true
sudo systemctl stop mysql nginx || true
sudo systemctl disable mysql nginx || true
sudo systemctl --no-pager --full status isuride-go | head -n 5
REMOTE_SCRIPT

if [ "${SKIP_DB_INIT}" = "1" ]; then
  printf '\n==> Skipping DB initialization (SKIP_DB_INIT=1)\n\n'
  printf 'Deployed webapp/go and webapp/sql to %s.\n\n' "${TARGET}"
  exit 0
fi

printf '\n==> Re-initializing DB schema via /api/initialize\n'
remote "curl -sf -X POST http://127.0.0.1:8080/api/initialize -H 'Content-Type: application/json' -d '{\"payment_server\":\"http://localhost:12345\"}'"

printf '\nDeployed webapp/go and webapp/sql to %s.\n\n' "${TARGET}"
