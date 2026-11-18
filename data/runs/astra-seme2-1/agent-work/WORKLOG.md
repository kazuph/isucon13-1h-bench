# ISUPipe 改善記録

`CONDITIONS.md`、`manual/cautionary_note.md`、`manual/isupipe.md` を確認して実施。
貸与された s1・s2・s3 の中だけでサービスを実行しています。

## 実装

- s1: nginx による HTTPS、静的ファイル、UDP DNS の中継。
- s2: Go API、DNS、索引付きメモリストア、永続化ログ。
- s3: bcrypt の生成・検証。アルゴリズムとコストは初期実装のまま。
- API の通常処理から SQL を除去。SQL は初期データの読み込み時だけ使用。
- 配信・ユーザー・タグ・コメント・リアクションを索引化。統計値は更新時に加減算。
- 更新を WAL に記録し、グループコミットのディスク同期完了後に成功応答。
- 初期化時にスナップショットと新しい WAL を作成し、CURRENT を原子的に切り替え。
- 再起動時にスナップショットと WAL を読み込み、索引を復元。
- DNS は既存ゾーンと API 登録済みユーザーを参照。不存在名は NXDOMAIN。
- アイコンの条件付き GET に対応。NG ワードの比較は実機 MySQL の utf8mb4_general_ci の比較値を使用。
- nginx のファイル記述子上限と接続上限を引き上げ、upstream keepalive を使用。

## 永続データと運用

実行ファイルは各サーバーの `/home/isucon/webapp/go/isupipe`。
s2 の永続データは `/home/isucon/webapp/state/` にあり、`CURRENT` が現在の世代を指します。
アプリケーションの書き込みはこのスナップショット・WAL が正本です。通常の書き込みは MySQL へ反映しません。
古い世代も保管してあります。初期化は `POST /api/initialize` で行います。

s1 は nginx、s2 と s3 は isupipe-go.service が自動起動します。
s1 の旧 Go サービスと s1・s2 の PowerDNS は無効化済みです。
反映済みの nginx 設定と systemd 設定を `configs/` に保存しています。

ビルド例（s2）:

```sh
cd /home/isucon/webapp/go
/home/isucon/local/golang/bin/go test ./...
/home/isucon/local/golang/bin/go build -o isupipe-new .
sudo systemctl stop isupipe-go
mv isupipe-new isupipe
sudo systemctl start isupipe-go
```

起動直後は WAL 復元があるため、HTTP/DNS の応答を確認してからベンチマークを開始してください。

## 検証

- `go test ./...`: 更新イベント・スナップショット復元・集計・LIKE 比較のテストに成功。
- s1・s2・s3 に `sudo reboot` を実行し、自動起動を確認。
- `tests/reboot_check.py`: 新規ユーザー、予約、コメント、投げ銭、リアクション、視聴、通報、NG ワード、画像を作成。
  再起動前後で11種類の JSON API 応答と画像 SHA256 が一致。
  既存セッションと bcrypt による再ログインも成功。
- ベンチ詳細は `logs/bench-*.log`、プロファイルは `logs/profile-*.log`。
- ベンチ判定が PASS の走行にも、初期実装同様、削除済みコメントの通報取得などの一般エラーは含まれます。

## 計測結果

| 構成 | スコア | 判定 |
|---|---:|---|
| 初期実装 | 9,049 | PASS |
| メモリストアへの全面移行 | 241,112 | PASS |
| 3台分離・索引化・nginx 上限修正 | 250,283 | PASS |
| グループコミット | 262,893 | PASS |
| 最終候補の再計測 | 262,523 | PASS |
| 全3台再起動後・最終構成 | **270,758** | **PASS** |

最終ベンチ: 2026-11-17 14:26:55〜14:28:02 JST。
初期スコア比 **29.92倍**。再起動後スコアは直前候補 262,523 点の約103.1%。
最終ベンチ後はサーバー構成を変更していません。
