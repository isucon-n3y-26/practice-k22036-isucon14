# プロビジョニング

ISUCON14の競技環境を構築するための構成管理・プロビジョニングツール群です。

## 1コマンド自動セットアップ（`setup.sh`）

`provisioning/setup.sh` を実行することで、Terraformによるインフラ構築、ベンチマーカーのビルド＆ECR push、Ansibleによる競技者EC2のプロビジョニングを一括で実行できます。

```sh
./provisioning/setup.sh
```

### `setup.sh` の内部処理フロー

1. **前提ツールの検証**:
   - `aws`, `terraform`, `ansible`, `ansible-playbook`, `docker`, `go`, `node`, `pnpm`, `task`, `make`, `jq`, `curl`, `ssh-keygen` がインストールされているか、Dockerデーモンが起動しているかをチェックします。
2. **SSH鍵の自動生成**:
   - `provisioning/terraform/.keys/isucon14_ed25519` を自動生成し、Terraformの変数経由でEC2の公開鍵として登録します。
3. **作業元IPアドレスの自動制限**:
   - 実行マシンのパブリックIPv4アドレスを自動取得し、EC2のSSH接続許可CIDRに設定します。
4. **成果物ビルド（Ansible用）**:
   - `provisioning/ansible/make_latest_files.sh` を呼び出し、フロントエンドのアセットビルド、ベンチマーカーバイナリ作成、`webapp.tar.gz` アーカイブの生成を行います。
5. **Fargate用ベンチマーカーDockerイメージのビルド**:
   - `bench/Dockerfile.fargate` を元に `linux/amd64` 向けイメージをローカルでビルドします。
6. **Terraform によるAWSインフラ構築**:
   - `terraform init` および `terraform apply` を実行し、VPC、ECR、ECS Fargateタスク定義、および公式Ubuntu 24.04 AMIのEC2（3台）を作成します。
7. **ECRへのベンチマーカーイメージPush**:
   - ECRへログインし、手順5でビルドしたイメージをプッシュします。
8. **EC2起動・初期化待機**:
   - Terraformのアウトプットから自動でAnsibleインベントリ（`provisioning/terraform/generated/inventory.ini`）を生成し、EC2のSSH接続可能状態および `cloud-init` の完了を待機します。
9. **Ansibleによる競技者環境プロビジョニング**:
   - `provisioning/ansible/application.yml` を実行し、Go 1.23.2のインストール、MySQL/Nginxの設定、DB初期化、各systemdサービス（`isuride-go`, `isuride-matcher`, `isuride-payment_mock`）の起動まで一貫して完了させます。
10. **ベンチマーク実行コマンドの案内**:
    - 完了後、すぐにベンチマークを実行できるコマンドを出力します。

詳細はルートの [README.md](../README.md) も参照してください。

## 1コマンド自動クリーンアップ（`cleanup.sh`）

環境を破棄してすべてのAWSリソースおよび一時ファイルを削除するには、以下を実行します。

```sh
./provisioning/cleanup.sh
# または確認プロンプトをスキップして即時破棄
./provisioning/cleanup.sh -y
```

## コンポーネント構成

- **`terraform/`**: AWSリソース（EC2 3台、VPC、ECR、ECS Fargateベンチマーカーなど）を定義するTerraformモジュール。詳細は [`terraform/README.md`](terraform/README.md) を参照。
- **`ansible/`**: 競技者環境およびベンチマーカー環境のプロビジョニング用Playbook/Role。詳細は [`ansible/README.md`](ansible/README.md) を参照。
- **`setup.sh`**: 上記を一連の流れで自動実行するオーケストレーションスクリプト。
- **`cleanup.sh`**: 作成したAWSリソース（Terraform管理）の破棄とローカル一時ファイル・Dockerイメージの削除を行うクリーンアップスクリプト。
- **`run_benchmark.sh`**: 指定した競技者向けにFargateベンチマークタスクを起動し、終了を待機した上でCloudWatch Logsのログをローカルにダウンロードするスクリプト。

```sh
./provisioning/run_benchmark.sh contestant-01
# ログは provisioning/terraform/generated/logs/contestant-01-<taskID>.log に保存されます
```

## 留意事項

- 作問・検証用の暫定として `trial.isucon14.net` の自己署名証明書を含んでいます。
- `setup.sh` は生成したSSH公開鍵と実行元のパブリックIPを `provisioning/terraform/setup.auto.tfvars.json`（Git管理対象外）に書き出します。これにより、`setup.sh` 実行後に別シェルで `terraform plan`/`apply`/`destroy` を手動実行した場合でも、キーペアやSSH許可CIDRの意図しない差分（削除）が発生しません。

### フロントエンド

`frontend/Makefile` に依存し、`pnpm run build` した結果（`frontend/dist` 等）をビルド成果物としてデプロイしています。

### API

`/api`, `/app`, `/provider`, `/chair` 宛のリクエストが 8080 にproxyされるようにnginxが構成されています。
APIサーバの動作に必要な環境変数は `provisioning/ansible/roles/isucon-user/templates/env.sh` で定義されています。

### DB

初期化処理として `webapp/sql/init.sh` を実行します。
初期化処理に変更や追加がある場合は修正が必要です。

