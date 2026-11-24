# 適用済みの構成

- s1 (10.99.0.11): nginx、PowerDNS、初期化・ユーザー登録用 Go アプリ。
- s2 (10.99.0.12): 通常 API 用 Go アプリ、DNS データベース `isudns`。
- s3 (10.99.0.13): アプリケーションデータベース `isupipe`。

nginx は `/api/initialize` と `/api/register` を s1:8080、その他の API を s2:8080 に転送します。両アプリは s3 の同じデータベースを利用します。DNS 登録・初期化は s1 の pdnsutil を通じて s2 のデータベースを更新します。

## ファイルの対応

| ローカル | 適用先 |
|---|---|
| `go/*.go` | s1・s2 `/home/isucon/webapp/go/` |
| `isupipe.conf` | s1 `/etc/nginx/sites-enabled/isupipe.conf` と `sites-available/isupipe.conf` |
| `pdns.conf` | s1 `/etc/powerdns/pdns.conf` |
| `gmysql-host.conf` | s1 `/etc/powerdns/pdns.d/gmysql-host.conf` |
| `mysql-s2.cnf` | s2 `/etc/mysql/mysql.conf.d/zz-isucon.cnf` |
| `mysql-s3.cnf` | s3 `/etc/mysql/mysql.conf.d/zz-isucon.cnf` |
| `pdns/init_zone.sh`, `pdns/u.isucon.dev.zone` (リポジトリ直下) | s1 `/home/isucon/webapp/pdns/` |

s1・s2 の `/home/isucon/env.sh` では `ISUCON13_MYSQL_DIALCONFIG_ADDRESS="10.99.0.13"` を設定しています。アプリは既存の `isupipe-go.service` で起動します。

ビルドコマンド（サーバー上）:

```sh
cd /home/isucon/webapp/go
/home/isucon/local/golang/bin/go test ./...
/home/isucon/local/golang/bin/go build -o isupipe.new .
mv isupipe.new isupipe
sudo systemctl restart isupipe-go
```

## スキーマと永続化

`indexes.sql`、`icon-hash.sql` はアプリ DB に適用済みです。`dns-index.sql` は DNS DB に適用済みです。既存環境への再実行は不要です。新規作成用の `sql/initdb.d/10_schema.sql` にもアプリの変更を反映しています。通常の初期化は TRUNCATE のため、索引と生成列を保持します。

画像ハッシュは MySQL の生成列に保存され、画像変更と同時に更新されます。DB の `innodb_flush_log_at_trx_commit=1` を維持しています。応答データの再利用は1リクエスト内のみで、データの正本は MySQL です。

DNS は A レコードの TTL を60秒としています。新規登録を阻害しないよう負のキャッシュとパケットキャッシュを無効化し、初期化時には正のキャッシュも purge します。

`original/` は初期設定の控え、`isupipe-two-apps.conf` は比較用の旧構成です。現在の設定は `isupipe.conf` です。

## 検証

検証用スクリプトはベンチマーク終了後に実行します。`--verify` は再起動後、次のベンチマークがデータを初期化する前に実行します。

- `bench`: 整合性確認を含む公式負荷試験。結果は `logs/bench-*.log`。
- `python3 tests/smoke.py`: 未登録名への問い合わせ後の DNS 登録、両アプリ間の画像更新、ETag による200/304を確認。
- `python3 tests/smoke.py --verify`: 上記で登録したユーザーと画像が再起動後も残っていることを確認。
- `logs/checksum-*-before.txt` と `logs/checksum-*-after.txt`: 全アプリテーブルと DNS の records/domains の再起動前後チェックサム。
