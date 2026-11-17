# ISUPipe の運用・検証

## 構成

- s1 (`10.99.0.11`): nginxによるTLS・静的ファイル・JSON圧縮・アイコン条件付きGETキャッシュ。Goの権威DNSは初期ゾーンの全Aレコードと登録済みユーザー名を保持する。未知の名前はNXDOMAIN。
- s2 (`10.99.0.12`): Go API。ユーザー、配信、タグ、コメント、リアクション、予約枠をメモリ上の索引で管理し、投げ銭などの集計を更新時に計算する。
- s3 (`10.99.0.13`): bcryptの生成・照合。初期実装と同じbcrypt・コスト4を使用する。

各台の `isupipe-network@10.99.0.1x` は起動時に自分の固定IPをARP通知する。再起動後に隣接機器が古いMAC対応を保持して初回のDNS要求が届かない状態を防ぐ。元の環境確認サービスは変更していない。

実行ファイルは各台の `/home/isucon/webapp/app/isupipe`。systemdのサービスはそれぞれ `isupipe-dns`、`isupipe-memory`、`isupipe-bcrypt`。

## 永続化と初期化

s2の `/home/isucon/webapp/data/baseline/snapshot.gob` が、提供された初期SQLから取り込んだ正規の初期データ。`current` は現行世代のディレクトリへのシンボリックリンク。各世代には `snapshot.gob` と `wal.jsonl` がある。

更新はログに書き込み、fdatasync完了後に成功応答する。同期は複数更新をまとめ、APIの読み取りロックを保持しない。起動時にスナップショットとログを再生する。初期化時には新しい世代を同期してからcurrentをアトミックに切り替える。過去世代は残している。

s1のDNS状態は `data/dns.json` に保存する。API起動時にも正規のユーザー集合から同期する。MySQLは初期データのインポート用に残しているが、通常のAPI処理では利用しない。競技中に更新された正規データは上記ログにある。

NGワードのLIKE判定は、貸与MySQLのutf8mb4_general_ciのUnicode照合重みを生成して埋め込んでいる。大文字小文字・アクセント・ワイルドカード・エスケープの扱いを維持する。モデレーション済みコメントはタイムラインと集計から削除し、報告の参照履歴を保持する。

## 配備

この作業ディレクトリから `scripts/deploy.sh` を実行する。s2上でテスト・ビルドし、3台に同じバイナリと設定を配る。既存の永続データはそのまま使う。

初期データを新規に作り直す場合のみ、s2で提供SQLの `sql/init.sh` を実行した後、`cd ~/webapp/app && ./isupipe import` を実行する。競技中のデータを維持したい場合には実行しない。

## 検証

- `bench`: 1本ずつ実行。結果は `logs/bench-*.log` に保存。
- `scripts/benchmark_summary.py`: 成否・スコア・仕様違反数を一覧化。
- `scripts/persistence_check.py prepare`: 実際のユーザー・画像・予約・投げ銭・リアクション・視聴・報告・モデレーションを作成し、API応答を保存。
- `scripts/icon_freshness.py`: 新しい画像ハッシュの304応答、古いハッシュの失効が2秒以内であることを検証し、元の画像に戻す。
- 3台を再起動した後、`scripts/persistence_check.py check`: 全API応答と画像ハッシュを比較。

アイコンのキャッシュキーはURLとIf-None-Matchを含み、上流のX-Accel-Expiresで現在の秒境界までを期限とし、実効有効期間を1秒未満に抑える。条件付きでないリクエストに304を流用しない。JSON圧縮とアクセス時間の計測設定は `config/performance.conf` にある。

計測用エンドポイントはs2のプライベートIP上のみで提供する。`http://10.99.0.12:8080/internal/durability` は同期回数・要求数・合計待ち時間、ポート6060はGo pprof。nginxの公開API経由ではこれらのパスに転送しない。
