# isucon13-1h-bench

ISUCON13 の過去問を、Mac mini（Apple Silicon）の上で Apple の `container` CLI だけを使い、1コマンドで作り直せる競技環境にしたもの。
AI エージェントに「開始から60分」を解かせ、モデルと指示ファイルの違いで点がどう変わるかを測る。Docker は使わない。

## 2026-11-15 の結果

3つのモデル × 3つの指示ファイルを、各1回ずつ60分流した。点は、60分後に3台を再起動してから流した最終ベンチの点。かっこの中は、AI が作業していた分数。

| モデル（CLI） | 素（空の指示ファイル） | 記事の要約（5行） | 攻め（12行） |
|---|---|---|---|
| Opus 5.5（Claude Code） | 342,388（60） | 408,390（60） | 483,230（60） |
| GPT-6.1-Sol（Codex） | 227,292（24） | 227,657（41） | 327,879（45） |
| GPT-6-Astra（Codex） | 146,993（17） | 132,143（18） | 276,147（24） |

初期実装の点は 8,352〜9,043点だった。各条件1回ずつなので、ぶれの大きさは測っていない。Astra の記事の要約は、1回目の60分以内の最高点が 204,487点で、上の2回目と大きく違った。

Astra の記事の要約と攻めの1回目（`astra-kawasaki-1`・`astra-seme-1`）は、最終ベンチが0点になった。再起動の後、アプリが DB につながる前に最終ベンチを流した測り方のミスで、2回目を流し直した。上の表は2回目の値。

指示ファイルの全文は `prompts/` にある。記事の要約は、川崎さんの記事「ISUCON2026 で優勝しました（準備編）」（https://zenn.dev/luvtechno/articles/isucon2026-preparation ）にある、当日の起動時の追記の要約5項目を文にしたもの。追記の原文は公開されていない。川崎さんが実際に使った指示ではなく、ブログに書かれていると分かっている部分だけで作ったので、実物とは大きく違う可能性がある前提で測っている。攻めは、記事の要約に7行を足したもの（`prompts/README.md`）。AI に渡した条件の全文は、各回の `data/runs/<回>/CONDITIONS.md`。AI には「競技時間は8時間」と伝え、60分で打ち切った。

### 記録の置き場所（`data/runs/<回>/`）

| ファイル | 中身 |
|---|---|
| `agent.jsonl` | AI の出力の全部（Claude Code の `stream-json`、Codex の `--json`）。打ったコマンドと結果が入っている |
| `bench.jsonl` | ベンチ1回につき1行。`final: true` が60分後の最終ベンチ |
| `events.jsonl`・`meta.json` | 起動・時間切れ・再起動などの出来事と、起動したコマンド・版 |
| `CONDITIONS.md`・`prompt-*.md` | その回に AI へ渡した条件と指示ファイル |
| `servers/s1〜s3/` | 60分後の各サーバーの Go のコードと、nginx・MySQL・PowerDNS・systemd の設定。実行ファイル・TLS の秘密鍵・MySQL の保守用パスワードは除いた |
| `code-reconstructed/`（`opus-seme-1` だけ） | 下の注を参照 |

`opus-seme-1` の AI は、書き直したアプリを `/home/isucon/app` に置いていて、当時の持ち帰り処理（`webapp` だけを写していた）に入らなかった。`code-reconstructed/` は、`agent.jsonl` に残った AI のファイル操作81手を、AI の VM と同じイメージの中で順に流し直して組み立てたもの（`scripts/replay-claude-log/`）。AI が付けた12のコミットの履歴（`history.bundle`）がそろい、Go のビルドが通る。最後のコミットの後に、計測を足す17行の変更が1つ残っている。60分で止めた時点でそれが動いていたかは分からない。今の `run.sh` は、各サーバーの `/home/isucon` 全体と AI の作業フォルダを持ち帰る。

グラフは `./plot.py data/runs -o results.html` で作れる（考察の `analysis.html` と、指示の全文もページに入る）。

## 次に試したいこと

- 全員がやったことを1体の Opus 5.5 に渡し、最初からやらせると、60分でどこまで行くか。
- その先で、本当に何をしてもよいとしたら、次に何で速くするか（Go から Rust、DB の自作、ベンチマーカーの改造など）。
- 全部入りの指示を、OpenCode Go の Muse 1.3 や DeepSeek などのモデルに渡して、GPT を超えられるか。指示には方針だけを書き、コードや問題を知っている前提の助言は書かない。

## ライセンス

`data/runs/*/servers/` のコードは、ISUCON13 の公式リポジトリ（https://github.com/isucon/isucon13 、MIT License、Copyright (c) 2023 ISUCON13 Contributors）の初期実装を、各回の AI が書き換えたもの。

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
- 流れ: 4台と AI の VM を作る → `CONDITIONS.md`（毎回生成）と指示ファイルを置く → AI 起動 → `RUN_MINUTES` 分で AI の VM を止める（AI が先に終われば、その時点で次へ）→ `restart-servers` → 最終ベンチ（`final: true`）→ 競技サーバーの `/home/isucon` 全体と `/etc/{nginx,mysql,powerdns,systemd/system,sysctl.d}` を持ち帰る → 全 VM を消す。AI の作業フォルダ（`agent-work/`）は、AI の VM を止める前に持ち帰る。公開用にまとめるときは `scripts/pack-results.sh` を Mac mini で流す。`KEEP=1` で消さない。
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

## Codex のログイン（実験専用）

Codex は使うたびに更新用のトークンを入れ替える。ほかの場所の `~/.codex/auth.json` を写して使うと、VM の中で入れ替わった時点で元のログインが切れる（2026-11-15、Mac mini の `~/.codex/auth.json` を写した3本目以降が `refresh token was already used` で全部失敗した）。
だから実験専用のログインを作り、Mac mini の `~/isucon13-1h-bench/secrets/codex/auth.json` に置く。`run.sh` は毎回これを VM に入れ、終わる前に VM の中で更新された `auth.json` を書き戻す。

```
mkdir -p ~/.codex-isubench && CODEX_HOME=~/.codex-isubench codex login
ssh mini 'mkdir -p ~/isucon13-1h-bench/secrets/codex && chmod 700 ~/isucon13-1h-bench/secrets/codex'
scp ~/.codex-isubench/auth.json mini:isucon13-1h-bench/secrets/codex/auth.json && rm -rf ~/.codex-isubench
```

ログインが切れていた回は `run.sh` が終了コード3を返し、`queue.sh` はその回を `runs/_invalid/` へ移して止まる。
