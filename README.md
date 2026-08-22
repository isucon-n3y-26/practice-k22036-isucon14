# ISUCON14 問題

## 当日に公開したマニュアルおよびアプリケーションについての説明

- [ISUCON14 当日マニュアル](./docs/manual.md)
- [ISUCON14 アプリケーションマニュアル](./docs/ISURIDE.md)

## ディレクトリ構成

```txt
.
+- bench          # ベンチマーカー
+- browsercheck   # ブラウザチェック用スクリプト
+- development    # 開発環境用のdocker compose
+- docs           # ドキュメント類
+- envcheck       # 競技環境の確認用プログラム
+- frontend       # 問題アプリケーションのフロントエンド
+- provisioning   # Terraform, Ansible, 一括セットアップスクリプト
+- webapp         # リファレンス実装
```

## TLS証明書について

ISUCON14で使用したTLS証明書は`provisioning/ansible/roles/nginx/files/etc/nginx/tls`以下にあります。

本証明書は有効期限が切れている可能性があります。定期的な更新については予定しておりません。

## ISUCON14で使用した競技環境

- 競技者 VM 3台
  - InstanceType: c5.large (2vCPU, 4GiB Mem) ※Terraform練習環境ではコスト削減のため c6a.large をデフォルト設定
  - VolumeType: gp3 20GB
- ベンチマーカー VM 1台
  - ECS Fargate (8vCPU, 8GB Mem) ※Terraform練習環境では16GB Mem

## AWS上での過去問環境の構築方法

### 一括自動セットアップ（推奨）

`./provisioning/setup.sh` を実行することで、Terraformによるインフラ構築（EC2 3台、VPC、ECR、ECS Fargateベンチマーカー定義など）、ベンチマーカーイメージのECR push、およびAnsibleによる競技者環境のプロビジョニングを1コマンドで一括実行できます。

#### 前提条件

ローカル環境に以下のコマンド・ランタイムがインストールされている必要があります。

- `aws` (AWS CLI: 認証情報が設定済みであること)
- `terraform` (v1.5以上)
- `ansible`, `ansible-playbook`
- `docker` (Docker Desktop等、デーモンが起動していること)
- `go`
- `node`, `pnpm`
- `task` ([Taskfile](https://taskfile.dev/))
- `make`
- `jq`
- `curl`, `ssh-keygen`

#### 実行コマンド

```sh
./provisioning/setup.sh
```

セットアップが完了すると、ベンチマーク実行コマンドが表示されます。ベンチマークタスクの起動・終了待機・ログのダウンロードまでを1コマンドで行う `run_benchmark.sh` の利用を推奨します。

```sh
./provisioning/run_benchmark.sh contestant-01
# ログは provisioning/terraform/generated/logs/ 以下に保存されます
```

タスクの起動だけを手動で行いたい場合は以下のコマンドも使えます（詳細は [`provisioning/terraform/README.md`](provisioning/terraform/README.md) 参照）。

```sh
cd provisioning/terraform
sh -c "$(terraform output -json benchmark_commands | jq -r '.["contestant-01"]')"
```

---

### Ubuntu ＋ Ansible 方式の注意点・ポイント

1. **公式Ubuntu 24.04 AMIの自動利用**
   - 独自の事前ビルドAMI（Packer）を必要とせず、Canonical公式のUbuntu 24.04 LTS AMI（SSM Parameter経由）から起動してAnsibleで直接プロビジョニングを行います。
2. **Go言語への絞り込み（高速プロビジョニング）**
   - 本構成ではセットアップ時間短縮のため、対象言語を **Go言語** に絞り込んでプロビジョニングを行います（Node.js, Python, Ruby, Rust, Perl, PHP等のビルドはスキップされます）。
   - 他の言語を使用したい場合は、`provisioning/ansible/roles/xbuildwebapp/tasks/main.yml` および `provisioning/ansible/roles/webapp/tasks/main.yaml` 内の該当タスクのコメントアウトを解除してください。
3. **SSH接続とセキュリティ設定**
   - `setup.sh` 実行時に、作業マシンのグローバルIPv4アドレスを自動取得してEC2のセキュリティグループ（SSH接続元許可）に設定します。
   - 専用のSSH鍵が `provisioning/terraform/.keys/isucon14_ed25519` に自動生成されます。
   - AWS Systems Manager (SSM) Session Manager経由の接続（`terraform output ssm_commands`）も可能です。
4. **SSHユーザーと権限**
   - EC2接続時のログインユーザーは `ubuntu` です。
   - アプリケーションの操作やコード編集を行う際は、`sudo su - isucon` で `isucon` ユーザーに切り替えて作業してください。
5. **クラウド初期化（cloud-init）待機**
   - EC2起動直後のOS初期化処理が完了するまでAnsible側で待機処理（`cloud-init status --wait`）を行います。
6. **リソースの削除（片付け）**
   - 練習終了後は、不要な課金を防ぐために一括クリーンアップスクリプトまたは Terraform でリソースを破棄してください。

     ```sh
     ./provisioning/cleanup.sh
     # または確認スキップ
     ./provisioning/cleanup.sh -y
     ```

---

## 既存サーバーへの直接Ansible適用

AWS EC2以外のUbuntu 24.04環境（ローカルVMや他クラウド）に対して直接セットアップを行う場合は、以下のようにAnsibleを実行してください。

```sh
cd provisioning/ansible
./make_latest_files.sh # フロントエンドおよびベンチマーカーのビルド
ansible-playbook -i inventory/localhost application.yml
```

ベンチマーカーを構築する場合:

```sh
ansible-playbook -i inventory/localhost benchmark.yml
```

---

## docker compose での環境構築（Go/Perl言語のみ）

作問時に利用した docker compose で環境を構築することもできます。ただし、スペックやTLS証明書の有無など競技環境とは異なります。

[Task](https://taskfile.dev/)を使用するので、事前にインストールしておいてください。

### アプリケーションの起動

```sh
task up
task go:run
```

### 負荷走行の実行

同一サーバー内でアプリケーションを起動している場合は、以下のコマンドで負荷走行を実行できます。

```sh
cd bench
task run-local
```

異なるホストに向けて負荷走行を行う場合は、以下のようなコマンドで負荷走行を実行できます。

```sh
cd bench
go run . run --target http://{{ 対象のIPアドレス }}:{{ 対象のポート番号 }} --payment-url http://{{ 対象のホストから見たベンチマーカーのIPアドレス }}:{{ 決済サーバーのポート番号 デフォルト:12345 }} -t 60
```

ベンチマーカーは負荷走行の実行中に決済サーバーとしても動作します。`--payment-url` は問題アプリケーションへ決済サーバーのURLを通知するためのオプションです。

ベンチマーカーを競技インスタンス上で動作させる場合は、競技インスタンス上で動作しているモックサーバーのポートと被ってしまうため `--payment-bind-port` オプションを指定してポートを変更してください。

静的ファイルのチェックに失敗する場合は `--skip-static-sanity-check` オプションを追加して実行することで、チェックをスキップできます。
（ただし、静的ファイル取得のリクエストもスキップされるため本番での負荷とは厳密には一致しなくなることに注意してください。）

## Links

- [ISUCON14 まとめ](https://isucon.net/archives/58818382.html)
