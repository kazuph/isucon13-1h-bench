# ISUPipe のデプロイ構成

| サーバー | 役割 |
| --- | --- |
| s1 (10.99.0.11) | Nginx、PowerDNS、Go アプリ。初期化とユーザー登録はこの Go プロセスに送る |
| s2 (10.99.0.12) | ISUPipe の MySQL。s1 と s3 のアプリから接続 |
| s3 (10.99.0.13) | Go アプリ。s1 の Nginx から API リクエストを受ける |

`go/` のソースを既存の `~/webapp/go` でビルドして s1 と s3 に配置した。`sql/indexes.sql` は初期スキーマに一度適用する移行で、インデックス、アイコンの生成ハッシュ列、通報から参照するモデレーション履歴を追加する。`sql/init.sh` と `sql/init.sql` は s1 の既存データ投入ファイルと共に使う。初期化は s2 の MySQL にシードを投入し、s1 の DNS ゾーンを差し替える。

`config/` には Nginx、MySQL、PowerDNS と Go の systemd ドロップインを収録した。MySQL の接続設定は s1 と s3 の `~/isupipe-db.env`、PowerDNS API の設定は s1 の `~/isupipe-pdns.env` と `/etc/powerdns/pdns.d/isupipe-api.conf` に保存してある。PowerDNS 4.9.18 の LMDB データは s1 の `/var/lib/powerdns/isupipe/` に永続化する。MySQL のコミット時の redo ログのフラッシュを有効にしている。

再デプロイ時は s1 の `/etc/powerdns/pdns.conf` から従来の `launch` と `gmysql-*` の設定を外し、`pdns.d/gmysql-host.conf` を読み込み対象から外す。s2 では同梱の追加設定に加え、配布時の `mysql.conf.d/mysqld.cnf` にある `bind-address` も `127.0.0.1,10.99.0.12` に設定する。

- [PowerDNS LMDB バックエンド](https://doc.powerdns.com/authoritative/backends/lmdb.html)
- [PowerDNS のゾーン更新 API](https://doc.powerdns.com/authoritative/http-api/zone.html)

## 計測

| 構成 | スコア | 結果 |
| --- | ---: | --- |
| 初期実装 | 8,284 | PASS |
| s1 単体で SQL とレスポンス生成を改善 | 130,172 | PASS |
| MySQL を s2 に分離し PowerDNS を LMDB に移行 | 145,325 | PASS |
| s3 に Go アプリを追加 | 156,973 | PASS |
| アイコンの単一クエリ化とローカル DNS API による登録 | 179,603 | PASS |
| 採用構成で全台再起動後 | 171,532 | PASS |

全台の再起動前後で、コメント数、投げ銭合計、アイコン数、ユーザー数が一致した。登録済みユーザーの A レコードとアイコンの SHA-256 も一致した。アイコンの条件付き GET は一致する ETag で 304、古い ETag で 200 を返した。

再起動後のベンチマークは名前解決成功 163,348 件、リクエストタイムアウト 0 件だった。
