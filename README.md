# isucon13-1h-bench

ISUCON13 の過去問を、Mac mini（Apple Silicon）の上で Apple の `container` CLI だけを使い、1コマンドで作り直せる競技環境にしたもの。
AI エージェントに「開始から60分」を解かせる実験の土台で、環境とベンチの入口だけを持つ（AI を走らせる部分は含まない）。Docker は使わない。

## 構成

| VM | 数 | 資源 | 中身 |
|---|---|---|---|
| `isu13-s1` `isu13-s2` `isu13-s3` | 3 | 各 2 CPU・4 GB、Ubuntu 22.04 arm64 | 公式の競技サーバーと同じ構成。systemd 上で MySQL 8、nginx、PowerDNS、Go の初期実装 `isupipe-go`、sshd が動く。初期データ投入済み |
| `isu13-bench` | 1 | 2 CPU・4 GB | 公式ベンチマーカー（linux/arm64 の実行ファイルのみ）と sshd |

ベンチは s1 だけを対象にする（nginx が 443、PowerDNS が 53 の入口。s2・s3 は同じ初期状態で待機する）。

## 使い方（Mac mini 上で実行）

```
./env.sh build                 # イメージを作る（初回とイメージ定義を変えた時だけ。up が無ければ自動で呼ぶ）
./env.sh up <run-id>           # 4台を全部捨てて作り直し、初期状態にする（約10秒）
./env.sh bench <run-id>        # ベンチを1回流し、runs/<run-id>/bench.jsonl に1行追記する（約80秒）
./env.sh restart-servers       # s1〜s3 を stop → start する。アプリは systemd が自動で上げる
./env.sh down                  # 4台を止めて消す
./env.sh ips                   # 4台の IP を表示する
```

手元のこのフォルダは `./sync.sh` で Mac mini の `~/isucon13-1h-bench/` に送る（鍵・CA・取得ソース・実行記録は送らない）。
初回の `build` が `keys/`（ssh 鍵）、`pki/`（自前 CA と証明書）を作り、公式リポジトリ `isucon/isucon13`（固定コミット `8f6afdc`）を `src/` に取得する。これらは git に入れない。

## ベンチの入口と記録

- `runs/<run-id>/bench.jsonl` に、ベンチ1回につき1行を追記する。キーは `run_id` `started_at` `ended_at`（UTC）`exit_code` `pass` `score` `messages` `language` `resolved_count`。
- 記録はベンチ VM の中ではなく Mac mini 側のフォルダ（`runs/` をベンチ VM に virtiofs でマウントしたもの）に書かれる。
- AI 用の VM から使う入口: ベンチ VM の `bench` ユーザーに `keys/bench_entry` の鍵で ssh する。`authorized_keys` に forced command（`command="/opt/bench/entry.sh"`）が付いていて、ssh に何のコマンドや pty を渡しても `entry.sh`（ベンチを1回流す）だけが走る。標準出力に結果の JSON 1行が返る。同時に2回は走らない（flock）。
  `ssh -i keys/bench_entry bench@<bench の IP>`
- `up` を同じ run-id でやり直すと、前の `bench.jsonl` は `bench.jsonl.<日時>` に退避される。
- 競技サーバーへは `ssh -i keys/isucon isucon@<s1〜s3 の IP>`（鍵のみ、`isucon` はパスワード無しで sudo 可）。
- IP は固定する。Apple container の IP は stop/start で変わるので、各 VM の eth0 に2つ目の固定 IP を足す（`images/common-net.sh`。ホスト名で決まり、起動の早い段階の systemd oneshot または entrypoint で実行）。s1=10.99.0.11、s2=.12、s3=.13、bench=.200、agent=.100。全 VM の `/etc/hosts` に `s1 s2 s3 bench agent` を書く。PowerDNS の A レコードと `ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS` は s1 の 10.99.0.11、ベンチの接続先も 10.99.0.11。

## 公式から変えた所

公式の本番サーバーは `provisioning/ansible/application.yml` で作られる。ここでは ansible を使わず、同じ内容を `images/server/Dockerfile` に書き直した。

| 何を | どう変えたか | なぜ |
|---|---|---|
| 構築方法 | ansible をやめ、Dockerfile でイメージ化。MySQL にスキーマと初期データ（`init.sql` と `initial_*.sql`）を焼き込む | VM を毎回まっさらに作るため。公式は `/api/initialize` で初回にデータを入れる |
| サービスの起動 | systemd を VM の PID 1 で動かす（`--cap-add ALL` が必要）。systemctl 互換スクリプトは使っていない | `sudo systemctl restart isupipe-go` を本番と同じに使えた |
| PowerDNS の版 | 公式の auth-48（4.8）から auth-49（4.9）に変更 | repo.powerdns.com の auth-48 は amd64 しか無く、arm64 があるのは auth-49 以降 |
| CPU アーキテクチャ | amd64 から arm64 | Apple Silicon の VM。Go は 1.21.2 の arm64 版を同じ場所 `/home/isucon/local/golang` に入れた |
| TLS の証明書 | 公式の証明書は期限切れのため、自前 CA で `*.u.isucon.dev` を署名して差し替え（nginx のファイル名と設定は公式と同じ）。ベンチ VM にその CA を信頼させる | ベンチは `--enable-ssl`（HTTPS、443）で公式の本番どおり nginx を入口にする。HTTP（8080）にすると nginx を通らず Go に直接当たるため HTTPS を選んだ |
| `ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS` | 公式は AWS の checkip で決める。ここでは `isucon-ip.service` が固定 IP を足し、s1 の固定 IP 10.99.0.11 を書く | AWS 以外では動かないため |
| `pdns.service` | `Requires=mysql.service` を足した | MySQL より先に起動して失敗するのを防ぐ |
| envcheck・globalip ロール | 入れていない | AWS 専用 |
| フロントエンド（`webapp/public`） | ビルドせず空ディレクトリ | ベンチは API だけを見る。必要なら `frontend/` を別途ビルドして置く |
| ベンチの実行 | 公式の `supervise`（ポータルから受ける）ではなく `bench run --enable-ssl --nameserver <s1 の IP>` を直接実行 | ポータルが無いため |

## AI を無人で走らせる（`run.sh`）

```
RUN_MINUTES=60 ./run.sh <run-id> <opus|astra|sol> <empty|kawasaki|seme>
```

- モデル: `opus` は Claude Code（`claude-opus-5-5`、`secrets/claude_oauth_token` の `CLAUDE_CODE_OAUTH_TOKEN` でログイン）、`astra` は Codex（`gpt-6-astra`）、`sol` は Codex（`gpt-6.1-sol`）。Codex は Mac mini の `~/.codex/auth.json` を AI の VM に写し、終わると VM ごと消える。
- 指示ファイルは `prompts/<名前>.md`。Claude は `CLAUDE.md`、Codex は `AGENTS.md` として AI の作業フォルダに置く。起動の1行は全条件で同じ。
- 足した CLI の引数は、自動実行に必要な権限の許可（Claude は `--dangerously-skip-permissions`、Codex は `--dangerously-bypass-approvals-and-sandbox`）と出力形式（`stream-json` / `--json`）だけ。
- 流れ: 4台と AI の VM を作る → `CONDITIONS.md`（毎回生成）と指示ファイルを置く → AI 起動 → `RUN_MINUTES` 分で AI の VM を止める（AI が先に終われば、その時点で次へ）→ `restart-servers` → 最終ベンチ（`final: true`）→ 競技サーバーの `webapp`・`/etc/{nginx,mysql,powerdns,systemd/system}` を持ち帰る → 全 VM を消す。`KEEP=1` で消さない。
- 見張り: 5秒おきに s1〜s3 の状態を見て、止まっていれば（AI の `sudo reboot` など）`container start` で戻し、`events.jsonl` に記録する。同じ見張りが Mac mini の負荷平均を `loadavg` に書く。
- 記録（`runs/<run-id>/`）: `agent.jsonl`（AI の出力）、`bench.jsonl`（`elapsed_s`・`host_loadavg`・`final` つき）、`events.jsonl`、`meta.json`（コマンド全文・版・時間）、`CONDITIONS.md`、`servers/s1〜s3`、`agent-versions.txt`。
- AI の VM は Node.js 22、`@anthropic-ai/claude-code`、`@openai/codex` を入れたイメージ（`images/agent/Dockerfile`）。入れた版は `agent-versions.txt` に出る。

## 結果のグラフ（`plot.py`）

```
./plot.py <runs フォルダ> [-o results.html] [--minutes 60]
```

`runs/<model>-<prompt>-<n>/` の `bench.jsonl` と `meta.json` から、外部ファイルを読まない1枚の HTML（SVG）を作る（Python 3 標準ライブラリのみ）。run-id がこの形でないもの（smoke など）は飛ばす。モデルごとにスコア推移（pass した最高点の階段、指示ごとに色固定）と、最終ベンチ（再起動後）を右の区画に置いたグラフ、その下に条件ごとの表（最高点・最終ベンチ・回数・5万/10万点を超えた分・負荷平均）を出す。

## 制約・注意

- VM の中から `sudo reboot` すると、その VM の PID 1 が終了して container ごと止まる。`run.sh` の見張りが数秒で `container start` する（`env.sh` だけで使うときは `./env.sh restart-servers`）。stop/start しても MySQL のデータ・変更したファイルは残る。
- `container build` の builder VM（`buildkit`、2 GB）はビルド後も動き続ける。メモリを空けたいときは `container builder stop`。
- ベンチの点数は run ごとにばらつく（同じ初期実装で 8,500〜9,700 点）。メッセージに `report` の 404 などが出ても `pass` は true になる。
