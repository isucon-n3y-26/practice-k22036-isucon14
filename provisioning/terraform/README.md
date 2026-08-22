# ISUCON14 practice environment on AWS

競技者EC2 3台と、必要なときだけ起動するベンチマーカーFargate Task、およびVPC・ECR・ECS・IAMを作成します。

## 構成

- 競技者: `c6a.large` × 3、gp3 20 GiB
  - Canonical公式のUbuntu 24.04 LTS AMI（SSM Parameter経由）を自動選択して起動
  - Ansibleによってアプリケーション環境を直接プロビジョニング
- ベンチマーカー: ECS Fargate
  - 8 vCPU、16 GiBメモリ
  - 常駐Serviceは作らず、ベンチマーク実行時だけTaskを起動
  - ベンチマーカーイメージはTerraformが作成するECRへpush
- リージョン: `ap-northeast-1`
- SSH/HTTPSのインターネット公開: デフォルトでは無効（`setup.sh` 実行時は自身のグローバルIPのみ自動許可）
- Fargate Taskから競技者のTCP/443への通信: 許可
- 競技者からFargate Taskの決済モック（TCP/12345）への通信: 許可
- 競技者EC2: AWS Systems Manager Session Managerで接続可能

> EC2、Fargate、EBS、Public IPv4、ECR、CloudWatch LogsなどのAWS利用料金が発生します。利用後は必ず`terraform destroy`を実行してください。
>
> ISUCON14本番の競技者VMは`c5.large`でした。この構成ではコストを抑えるため、同じ2 vCPU・4 GiBの`c6a.large`を使用します。

## クイックスタート（推奨）

リポジトリルートから `./provisioning/setup.sh` を実行するだけで、Terraformの適用、ECRへのイメージpush、Ansibleプロビジョニングまで一括で完了します。

```sh
./provisioning/setup.sh
```

---

## 手動での段階的セットアップ手順

`setup.sh` を使わず各ステップを手動で実行する場合の手順です。

### 前提条件

- Terraform 1.5以降
- Ansible (`ansible`, `ansible-playbook`)
- AWS CLI（認証情報設定済み）
- Docker / Docker Buildx
- Go、Node.js、`pnpm`、go-task、`make`
- `jq`
- Session Managerを使う場合はローカルにSession Manager pluginがあること

### 1. アプリケーション成果物のビルド

```sh
cd provisioning/ansible
./make_latest_files.sh
```

### 2. AWSリソースの作成 (Terraform)

```sh
cd provisioning/terraform
cp terraform.tfvars.example terraform.tfvars
terraform init
terraform plan
terraform apply
```

AWS CLIプロファイル、キーペア、接続元CIDRなどが必要なら`terraform.tfvars`を編集します。SSHを開けなくても競技者EC2にはSSMで接続できます。

### 3. ベンチマーカーイメージのビルドとECRへのpush

ECRへログインします。

```sh
aws ecr get-login-password --region ap-northeast-1 \
  | docker login \
      --username AWS \
      --password-stdin "$(terraform output -raw benchmarker_ecr_registry)"
```

`linux/amd64`イメージをビルドしてpushします。

```sh
docker buildx build \
  --platform linux/amd64 \
  --file ../../bench/Dockerfile.fargate \
  --tag "$(terraform output -raw benchmarker_image_uri)" \
  --push \
  ../../bench
```

ベンチマーカーのソースを変更した場合は、同じコマンドでイメージを再度pushしてください。

### 4. Ansibleによる競技者環境のプロビジョニング

EC2のPublic IPを確認し、インベントリを作成してAnsibleを実行します。

```sh
# EC2へのSSH接続確認＆cloud-init完了待機後、実行
ansible-playbook -i <inventory_file> ../ansible/application.yml
```

---

## ベンチマーク実行

`../run_benchmark.sh` を使用してください。指定した競技者向けにFargate Taskを起動し、終了を待機した上で、そのタスク専用のCloudWatch Logsをローカルファイルにダウンロードするところまでを1コマンドで実行します。

```sh
../run_benchmark.sh contestant-01
# ログは ./generated/logs/contestant-01-<taskID>.log に保存されます
```

Taskはベンチマーク終了後に自動停止します。常駐Serviceはないため、実行していない間のFargate CPU・メモリ料金は発生しません。

### 手動で実行する場合

競技者ごとのFargate Task起動コマンドはoutputとして確認できます。

```sh
terraform output benchmark_commands
```

例えば`contestant-01`を対象に実行する場合は、対応するコマンドをJSON出力から取得して実行します。Task Definitionには対象競技者のプライベートIPが設定済みです。

```sh
sh -c "$(terraform output -json benchmark_commands | jq -r '.\"contestant-01\"')"
```

ログは AWS コンソールの CloudWatch Logs（ロググループ: `/ecs/isucon14-practice/benchmarker`）または CLI で確認します。

```sh
aws logs tail /ecs/isucon14-practice/benchmarker --follow
```

## 競技者EC2への接続

```sh
terraform output ssm_commands
```

または、Public IP経由のSSH・ブラウザアクセスが必要な場合は、接続元だけを許可します。

```hcl
key_name            = "my-key-pair"
allowed_ssh_cidrs   = ["203.0.113.10/32"]
allowed_https_cidrs = ["203.0.113.10/32"]
```

SSHユーザーは `ubuntu` です。ログイン後は `sudo su - isucon` で `isucon` ユーザーに切り替えて作業してください。

## ローカルのwebapp/goをEC2に反映する

ローカルの`webapp/go`を編集して、そのままEC2に反映・再起動したい場合は`../deploy_webapp.sh`が使えます。`rsync`でファイルを同期し、リモートでビルドして`isuride-go`を再起動します。

```sh
../deploy_webapp.sh contestant-01
```

## 削除（片付け）

一括クリーンアップスクリプトを使用する場合:

```sh
./provisioning/cleanup.sh
# または確認プロンプトをスキップ
./provisioning/cleanup.sh -y
```

または Terraform で直接リソースを破棄する場合:

```sh
cd provisioning/terraform
terraform destroy
```
