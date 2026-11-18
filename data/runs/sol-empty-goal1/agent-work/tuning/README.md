# ISUPipe 改善構成

| サーバー | 役割 |
|---|---|
| s1 / 10.99.0.11 | nginx、Go API、PowerDNS、DNS用SQLite |
| s2 / 10.99.0.12 | アプリ用MySQL |
| s3 / 10.99.0.13 | Go API |

s1 nginx のAPI振り分けは s1:s3=1:3。DNS登録を行う /api/register と /api/initialize は s1 固定。
s1/s3 の env.sh は ISUCON13_MYSQL_DIALCONFIG_ADDRESS="10.99.0.12"。DNSの応答IPは10.99.0.11。
s2のGoサービスは無効化済み。

## 適用済みファイル

| ローカルファイル | 配置先 |
|---|---|
| config/s1-nginx-weighted.conf | s1:/etc/nginx/sites-enabled/isupipe.conf |
| config/s2-mysql.cnf | s2:/etc/mysql/mysql.conf.d/zz-isucon-tuning.cnf |
| config/pdns-sqlite.conf | s1:/etc/powerdns/pdns.conf |
| init.sh | s1:/home/isucon/webapp/sql/init.sh（実行権限あり） |
| go/*.go | s1/s3:/home/isucon/webapp/go/ |

アプリの再配布は ./deploy-app.sh。CGO無効・trimpathでビルドしてバイナリを置換し、サービスを再起動する。
SQL追加索引 indexes.sql と03-migrations.sql は適用済み。再適用しない。

## DNSのSQLite移行

pdns-backend-sqlite3 と sqlite3 を s1 に導入済み。
公式schema.sqlite3.sqlを使い、migrate-dns-sqlite.pyで既存MySQLのDNSデータ全7テーブルを変換した。
isudns.sqlite3 は移行時のスナップショットで、現在の稼働データではない。

稼働DBは s1:/var/lib/powerdns/isudns.sqlite3。所有者pdns:pdns、モード0660。
ディレクトリは0775、isuconはpdnsグループ所属。Goサービスの補助グループにも反映済み。
PowerDNSはWAL、synchronous=2（FULL）で同期書き込みする。
旧 /etc/powerdns/pdns.d/gmysql-host.conf は /etc/powerdns/gmysql-host.conf.mysql-backup に退避した。

DNSキャッシュは正20秒、負1秒。登録時と初期化時に sudo pdns_control purge で消去する。
ゾーンの公開TTLは元の設定を維持。pdnsutilによる登録・初期化の手順も維持する。

## 主な変更

- 検索・結合に索引を追加し、ランキングを集約SQLで算出。
- アイコンSHA256をDB生成列で保存し、If-None-Matchを処理。
- アイコン更新はREAD COMMITTEDとユーザー行ロックでデッドロックを防止。
- NGワード判定と削除をSQLで一括処理。元のLIKE照合順序を維持。
- ユーザー・配信情報をリクエスト内で再利用し、配信タグをJOINで取得。
- ユーザー・配信情報の共有キャッシュは各500ms。配信に含まれる所有者アイコンも合計1秒以内のキャッシュ経過を上限とする。
- キャッシュ期限はリクエスト開始時刻に固定。成功した処理の結果だけを共有し、ヒットしたデータの期限を延長しない。初期化時に両APIサーバーで消去する。
- 予約枠の索引走査を予約期間に限定。行ロックと枠数検証は維持。
- 削除済みコメントの通報を一覧で参照して500になる問題を修正。統計用の通報履歴は保持。
- s2のInnoDBバッファは2GB。レプリケーション用バイナリログを停止し、InnoDBのコミットごとの同期書き込みは維持。

bcryptのアルゴリズム・コストは変更していない。
一括レスポンス取得、DNS用MySQLのs3移設、JSON gzipは比較後に採用しなかった。

## 検証

ベンチマーク結果は ../results/。
キャッシュの公開タイミング・期限・並行リセットは response_cache_test.go で検証。
s3で CGO_ENABLED=1 /home/isucon/local/golang/bin/go test -race ./... が成功済み。
check-icon-cache.py はアイコン画像・ユーザー情報・配信の所有者情報・304応答を検証する。
check-persistence.py はアプリ全12テーブルのチェックサム、DNS全テーブルの論理ダンプのハッシュ、直近のユーザー・配信・画像・売上・DNSを再起動前後で比較する。
