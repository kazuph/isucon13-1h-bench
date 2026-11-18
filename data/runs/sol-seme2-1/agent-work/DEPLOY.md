# 配置メモ

現在、配置・有効化済み。稼働中の構成は RESULTS.md を参照。

初期状態から再構築する際の要点:

1. s3 の MySQL に config/database-setup.sql と config/migrations.sql を適用する（後者の使用DBは isupipe）。MySQL の設定は config/mysqld.cnf、config/mysql.cnf、config/mysql-persist.json を参照。アプリ用 DB と DNS 用 DB はともに s3。
2. s2 の /home/isucon/webapp/go に app/ のソースを配置し、~/local/golang/bin/go build -o isupipe . でビルド。env.sh は config/app.env、sql/init.sql は config/sql/init.sql を使用する。
3. s2 の PowerDNS CLI の接続設定を gmysql-host=10.99.0.13 とする。/etc/powerdns/pdns.d/ 内の設定による上書きにも注意する。初期化のゾーン投入で pdnsutil を使用する。
4. 同じビルド成果物を s1 の /home/isucon/webapp/go/isupipe-dns に配置。config/dns.env と config/isupipe-dns.service を配置し、元の pdns と isupipe-go を無効化して isupipe-dns を有効化する。
5. s1 の nginx に config/nginx.conf と config/nginx-site.conf を配置する。静的ファイル・証明書は初期配置のものを使用。
6. network/announce.go をビルドし、3台それぞれの /home/isucon/arp-announce に配置する。config/isupipe-arp-announce@.service をインストールし、各サーバーの固定IPをインスタンス名として有効化する（例: s1 では isupipe-arp-announce@10.99.0.11.service）。この環境の再起動時MAC変更に対して自IPv4のARPを通知する。
7. s2 の isupipe-go、s1 の nginx と isupipe-dns、s3 の mysql の自動起動を確認。POST /api/initialize がゾーンとDBを初期化する。切り替え直後はベンチ前のDNS検査に必要な初期レコードも投入しておく。

検証:

- Go: app/ で go vet ./...、go test -run TestLikeMatchesMySQL -count=1 .（競技DBへの接続を使用）。
- SQL照合: checks/local_verify.py を作業端末から実行。127.0.0.1:18080 → s2:127.0.0.1:8080 のSSHトンネルを使用し、SQLは ssh s3 経由で実行する。
- 保存確認: checks/persist.go を s2 でビルドし、capture と verify を用いて再起動前後のレスポンスを比較する。
- ベンチ: 作業端末の bench。起動直後はAPI・DNSの準備完了を確認してから実行する。
