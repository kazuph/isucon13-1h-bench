# isucon13-1h-bench

ISUCON13 の過去問を、Mac mini（Apple Silicon）の上で Apple の `container` CLI だけを使い、1コマンドで作り直せる競技環境にしたもの。
AI エージェントに「開始から60分」を解かせ、モデルと指示ファイルの違いで点がどう変わるかを測る。Docker は使わない。

## 2026-11-15 の結果

3つのモデル × 3つの指示ファイルを、各1回ずつ60分流した。点は、60分後に3台を再起動してから流した最終ベンチの点。かっこの中は、AI が作業していた分数。

| モデル（CLI） | 素（空の指示ファイル） | 川崎さん版（5行） | 攻め（12行） |
|---|---|---|---|
| Opus 5.5（Claude Code） | 342,388（60） | 408,390（60） | 483,230（60） |
| GPT-6.1-Sol（Codex） | 227,292（24） | 227,657（41） | 327,879（45） |
| GPT-6-Astra（Codex） | 146,993（17） | 132,143（18） | 276,147（24） |

初期実装の点は 8,352〜9,043点だった。各条件1回ずつなので、ぶれの大きさは測っていない。Astra の川崎さん版は、1回目の60分以内の最高点が 204,487点で、上の2回目と大きく違った。

Astra の川崎さん版と攻めの1回目（`astra-kawasaki-1`・`astra-seme-1`）は、最終ベンチが0点になった。再起動の後、アプリが DB につながる前に最終ベンチを流した測り方のミスで、2回目を流し直した。上の表は2回目の値。

指示ファイルの全文は `prompts/` にある。川崎さん版は、川崎さんの記事「ISUCON2026 で優勝しました（準備編）」（https://zenn.dev/luvtechno/articles/isucon2026-preparation ）にある、当日の起動時の追記の要約5項目を文にしたもの。追記の原文は公開されていない。攻めは、川崎さん版に7行を足したもの（`prompts/README.md`）。AI に渡した条件の全文は、各回の `data/runs/<回>/CONDITIONS.md`。AI には「競技時間は8時間」と伝え、60分で打ち切った。

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

## 2026-11-17 の追加の結果

11/15 の結果から、2つの疑問を確かめた。各条件1回ずつで、ぶれは測っていない。点は11/15と同じく、60分後に3台を再起動してから流した最終ベンチの点。かっこの中は、AI が作業していた分数。

### Codex に `/goal` を付けたら（`GOAL=1`）

GPT-6.1-Sol と GPT-6-Astra は、11/15 の回で60分より前に自分で作業を終えていた。Codex は `/goal` を書かないと途中で止まることが多いので、Codex を対話画面で起動し、起動の1行を `/goal <起動の1行>` として打った回を流した（`codex exec` に `/goal` を渡しても、続きのターンが始まらない。`run.sh` の `GOAL=1` の説明を参照）。指示ファイルは11/15と同じ。

| 回 | `/goal` 無し（11/15） | `/goal` 付き | `/goal` 付きの終わり方 |
|---|---|---|---|
| GPT-6.1-Sol・素 | 227,292（24） | 67,201（60） | 時間切れ |
| GPT-6-Astra・素 | 146,993（17） | 181,029（25） | AI がゴール達成と判断 |
| GPT-6.1-Sol・攻め | 327,879（45） | 247,858（40） | AI がゴール達成と判断 |
| GPT-6-Astra・攻め | 276,147（24） | 115,916（60） | 時間切れ（60分以内の最高は 249,013） |

`/goal` を付けても、点が上がる傾向は見えなかった。ゴールの文（「CONDITIONS.md を読み、今回の条件を確認してから、ISUCON を解いてください。」）には時間の条件が無く、AI が「達成した」と判断した時点で止まった。川崎さん版の2本（`/goal` 付き）は流していない。

### `/goal` に時間の条件を足したら（`GOAL=1`・`GOAL_TEXT`、2026-11-18）

上の `/goal` の4本では、ゴールの文に時間の条件が無く、2本は AI が「達成した」と判断して早く止まった。そこで、ゴールの文の後ろに「競技の終了時刻まで作業を止めず、点を上げ続けてください。」を足した（`run.sh` の `GOAL_TEXT`）。AI には競技は8時間と伝えてあるので、「60分」とは書いていない。組み合わせと指示ファイルは上の4本と同じ。

| 回 | `/goal` 無し（11/15） | `/goal`（時間の条件なし） | `/goal`＋時間の条件 |
|---|---|---|---|
| GPT-6.1-Sol・素 | 227,292（24） | 67,201（60） | 208,455（60） |
| GPT-6-Astra・素 | 146,993（17） | 181,029（25） | 264,468（60） |
| GPT-6.1-Sol・攻め | 327,879（45） | 247,858（40） | 270,985（60） |
| GPT-6-Astra・攻め | 276,147（24） | 115,916（60） | 413,093（60） |

4本とも60分を使い切った。GPT-6-Astra は2本とも上がり、攻めの 413,093点は GPT-6-Astra の回の中で一番高い。GPT-6.1-Sol は2本とも `/goal` 無しより下がった。各1回ずつ。記録は `data/runs/*-goaltime1`。`astra-empty-goaltime1` の作業フォルダにあった Go の本体一式（`toolchain/`、214MB）は、AI が書いたものではないので入れていない。

### 手の名前を消した攻め（seme2、9行）

11/15 の攻め（12行）には、「書き直し・DB の作り直し・オンメモリ化・台の構成替えも含めて」と具体的な手の名前を書いていた。オンメモリ化は ISUCON の上位の人たちがよく使う定石で、「人間がよくやる定石を step by step でやることは禁止」と書きながら、その名前を書いていたことになる。そこで手の名前などを消した9行（`prompts/seme2.md`）で流し直した。`/goal` は付けていない（11/15 の攻めの回とそろえた）。

| モデル | 攻め（12行、11/15） | seme2（9行） |
|---|---|---|
| Opus 5.5 | 483,230（60） | 408,810（38） |
| GPT-6.1-Sol | 327,879（45） | 243,221（43） |
| GPT-6-Astra | 276,147（24） | 266,916（21） |

手の名前を書かなくても、3つとも読み出しをメモリから返す形に作り変えた。Opus 5.5 は全データを `map` に持ち、書き込みはキューを通して MySQL へ送る（`data/runs/opus-seme2-1/agent-work/src/app/store.go`・`writer.go`）。GPT-6-Astra は通常の API から SQL を外し、メモリの索引と書き込みのログに持つ（`memory_store.go`）。GPT-6.1-Sol は読み出しだけをメモリから返し、書き込みは MySQL に残した（`memory.go`、11/15 の攻めと同じ形）。Opus 5.5 が60分より前に自分で作業を終えたのは、この回が初めて。最後のメッセージでは「ベンチの送り手が限界で、こちらのサーバーではない」と判断している。

seme2 で12行から変えたところ:
- 消した: 「15分前後で丸ごと書き直せる（過去に12分で書き直した実測がある）」、「最初の10分で、…最終形（データの持ち方、3台の役割）を決め、最初からそこへ直行する」、手の名前4つ、「変更はまとめて入れてよい。コミットは細かく分け、…」、「ベンチはこまめに回し、最高点のコミットにタグを付ける。…」
- 書き換えた: 「定石にとらわれず、…究極の一手を最初から打つ」を「リスクや実装時間を気にしない丸ごとの書き直しを含む究極の一手を最初から打つ」にした。

### 手の名前だけを消した攻め（seme3、12行）

seme2 は手の名前のほかの文も消していたので、点の差が手の名前のせいかを分けられなかった。そこで、攻め12行の8行目から手の名前4つだけを消した `prompts/seme3.md` で流した（ほかの11行は同じ。`/goal` 無し）。

| モデル | 攻め（12行） | seme2（9行） | seme3（12行・名前だけ消した） |
|---|---|---|---|
| Opus 5.5 | 483,230（60） | 408,810（38） | 131,307（60・別作業と衝突） / 427,757（60・11/24 無干渉で再試） |
| GPT-6.1-Sol | 327,879（45） | 243,221（43） | 424,420（46） |
| GPT-6-Astra | 276,147（24） | 266,916（21） | 287,021（56） |

書き込みの持ち方（コードで確認）:

| モデル | 攻め（12行） | seme2 | seme3 |
|---|---|---|---|
| Opus 5.5 | MySQL を外し、全データをメモリ。書き込みはファイルに追記 | 全データをメモリ。書き込みはキューで MySQL へ | 全データをメモリ。書き込みはキューで MySQL へ（`main.go` の `persist`） |
| GPT-6.1-Sol | 読み出しはメモリ、書き込みは MySQL | 同じ | 全データをメモリ。通常の API で SQL を呼ばず、書き込みはファイルのログ（`state.wal`） |
| GPT-6-Astra | MySQL を外し、全データをメモリ | 通常の API から SQL を外し、メモリと書き込みのログ | 同じ（`fast/durable.go` の WAL） |

Opus 5.5 の seme3 の1回目（`opus-seme3-1`）は、60分の途中では最高 378,321点まで出ていたが、最終ベンチ開始の2秒後に同じ Mac mini で別セッションの Go 並列テストが走り（ロードアベレージ 30〜124）、CPU を取り合って 131,307点に落ちていた。11/24 に無干渉状態（`load 2.51`）で再試した `opus-seme3-2` では **427,757点（60分完走・PASS）** だった。各1回ずつなので、手の名前の有無で点がどう変わるかは、この本数だけでは言えない。

## 2026-11-20〜24 の追加の結果（GPT-6.2・Gemini 4 Neon・Opus 再試）

### GPT-6.2（`gpt-6.2-sol`・`gpt-6.2-astra`、`codex-cli 0.171.0`）

`/goal` 無し（素の `codex exec`）と、11/18 と同じ文の「時間の条件つき `/goal`」（`GOAL=1 NUDGE=1`、止まったら尻叩きする見張り付き。4本とも尻叩きは0回で60分完走）の計8本。

| 回 | `/goal` 無し | `/goal`＋時間の条件（同じ文） | 尻叩き回数 |
|---|---|---|---|
| GPT-6.2-Sol・素 | 179,462（19） | 245,326（60） | 0 |
| GPT-6.2-Astra・素 | 241,082（21） | 323,651（60） | 0 |
| GPT-6.2-Sol・攻め | 92,092（21） | 262,725（60） | 0 |
| GPT-6.2-Astra・攻め | 304,873（22） | 404,831（60） | 0 |

`/goal` 無しでは 6.2 でも4本とも 19〜22分で自分から終了した。時間の条件つき `/goal` を入れると4本とも一度も止まらず60分完走した。最高は GPT-6.2-Astra・攻めの 404,831点（GPT-6-Astra の 413,093点とほぼ同じ）。

### Gemini 4 Neon (High)（`agy 1.5.13`・`/goal` 無し）

`agy -p`（単体エージェント・`/goal` 無し）で、特別な指示なし（`empty`）と攻めの指示（`seme`）を測った。

| run-id | プロンプト | 稼働時間 | 最終ベンチ | 備考 |
|---|---|---|---|---|
| `neon-empty-1` | 特別な指示なし（`empty`） | 60.2分 | **437,417** | 指示なし部門1位。`webapp/icons`（270MB）は公開用パックから除外 |
| `neon-seme-1` | 攻めの指示（`seme`） | 41.7分 | 495,002 | 連続2本目でクォータ上限到達により終了 |
| `neon-seme-2` | 攻めの指示（`seme`） | 60.1分 | **502,510** | 60分フル完走・**全モデル初の50万点突破・総合1位** |

### 川崎さん版について

川崎さん版（5行）は、記事にある当日の構成（lite）のうち、起動時の指示（55行、全文は非公開）の要約だけから作った。lite には、ほかに次の仕組みがあり、この実験には入れていない。
- ターンを終えようとしたら、観点を添えて続けさせる Stop フック（記事の表では「持ち時間を残して手を止める」への手当て）
- 頭打ちのときに、記録を観点の一覧と突き合わせて検討し直す観点を返す、監査役のサブエージェント（観点の一覧の中身は非公開）
- 時刻で出す指示（60分ごとに監査役を促す、残り40分で終盤の確認を促す）と、API エラーで continue を送る監視のプロセス

### 記録

`data/runs/` の `*-goal1`（`/goal` 付きの4本）と `*-seme2-1`（seme2 の3本）。この回から、AI の作業フォルダ（`agent-work/`、AI が書いたコードとメモ。git の履歴があれば `agent-work.history.bundle`）と、各サーバーの `webapp` の公式以外のフォルダも入れた。`/goal` 付きの回の `agent.jsonl` は Codex のセッションの記録で、Codex の組み込みの指示と内部の状態は除いた。画面の履歴は `agent-screen.txt`。

## 次に試したいこと

- 川崎さんの当日の構成にあった Stop フック（ターンを終えようとしたら、観点を添えて続けさせる）を入れたら、GPT-6.1-Sol と GPT-6-Astra は60分を使い切り、点が上がるか。
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
- `GOAL=1`（Codex だけ）: Codex を対話画面（TUI）で tmux の中に起動し、起動の1行を `/goal <起動の1行>` として打つ。`codex exec` に `/goal` を渡すと、ゴールは登録されるが続きのターンが始まらずに終わる（2026-11-17、codex-cli 0.169.0 で確かめた）。対話画面ならゴールの達成まで次のターンが自動で続く。画面の最下行に `Goal achieved` が出たら AI の作業の終わりとみなす。記録は、Codex のセッションの jsonl を `agent.jsonl` に、画面の履歴を `agent-screen.txt` に写す。tmux は時計を始める前に AI の VM へ入れる。
- `PAUSE` ファイル: このフォルダに `PAUSE` があると、`run.sh` は始める前に待つ（待っている間は `PAUSED` を置く）。`queue.sh` で流している途中に、Mac mini で別の重い処理をするとき、回と回の間で止めるために使う。
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
