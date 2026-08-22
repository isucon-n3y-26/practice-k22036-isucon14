#!/bin/sh
# ==============================================================================
# ISUCON14 ECS Fargate Benchmarker Entrypoint Script
#
# 【役割】
# ベンチマーカーは負荷走行中に決済モックサーバー（デフォルト: 12345ポート）としても動作します。
# 競技者EC2がこの決済モックへアクセスできるよう、ECS Container Metadata Service (v4) から
# Fargateタスク自身のプライベートIPアドレスを動的に取得し、ベンチマーカーコマンド引数に
# `--payment-url http://<private_ip>:12345` を付与して実行します。
# ==============================================================================

set -eu

# 1. ECS Container Metadata URI (v4) の存在確認
# Fargateタスク環境では自動的に ECS_CONTAINER_METADATA_URI_V4 が注入されます
if [ -z "${ECS_CONTAINER_METADATA_URI_V4:-}" ]; then
  echo "ECS_CONTAINER_METADATA_URI_V4 is not set" >&2
  exit 1
fi

# 2. タスクメタデータエンドポイントからプライベートIPアドレスを取得（最大30秒リトライ）
# 起動直後はネットワーク情報がメタデータに反映されるまで僅かに遅延する場合があるためリトライします
metadata_url="${ECS_CONTAINER_METADATA_URI_V4}/task"
private_ip=""
attempt=0
while [ -z "$private_ip" ] && [ "$attempt" -lt 30 ]; do
  private_ip="$(curl --fail --silent "$metadata_url" 2>/dev/null | jq -r '.Containers[0].Networks[0].IPv4Addresses[0] // empty' || true)"
  attempt=$((attempt + 1))
  if [ -z "$private_ip" ]; then
    sleep 1
  fi
done

# 3. IPアドレス取得失敗時のエラーハンドリング
if [ -z "$private_ip" ]; then
  echo "Failed to determine the Fargate task private IP" >&2
  exit 1
fi

# 4. 取得したプライベートIPを指定してベンチマーカーを起動
# タスク定義の Command ($@) に --payment-url オプションを追加して引き継ぎます
exec "$@" --payment-url "http://${private_ip}:12345"

