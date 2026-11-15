# ISUCON13 競技サーバー(公式 provisioning/ansible の application.yml 相当を1つのイメージにしたもの)
# 公式の ansible は使わず、同じ内容を Dockerfile で書き直している。差分は README の「公式から変えた所」を参照。
FROM ubuntu:22.04
ENV DEBIAN_FRONTEND=noninteractive

# ビルド中にサービスを起動させない(起動は実機の systemd に任せる)
RUN printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d && chmod +x /usr/sbin/policy-rc.d

# base + apt + nginx + mysql(公式は mysql-server / nginx を Ubuntu のパッケージで入れる)
RUN apt-get update && apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus openssh-server sudo ca-certificates curl gnupg \
      iproute2 iputils-ping dnsutils procps less vim wget htop jq git make rsync lsof net-tools \
      nginx mysql-server \
    && rm -rf /var/lib/apt/lists/*

# PowerDNS(公式は repo.powerdns.com の jammy-auth-48 だが amd64 のみ。arm64 があるのは auth-49 以降なので auth-49)
RUN install -d -m 0755 /etc/apt/keyrings \
    && curl -fsSL https://repo.powerdns.com/FD380FBB-pub.asc -o /etc/apt/keyrings/auth-49-pub.asc \
    && echo "deb [signed-by=/etc/apt/keyrings/auth-49-pub.asc arch=arm64] http://repo.powerdns.com/ubuntu jammy-auth-49 main" > /etc/apt/sources.list.d/pdns.list \
    && printf 'Package: auth*\nPin: origin repo.powerdns.com\nPin-Priority: 600\n' > /etc/apt/preferences.d/auth-49 \
    && apt-get update && apt-get install -y pdns-server pdns-backend-mysql \
    && rm -rf /var/lib/apt/lists/*

# isucon ユーザー(uid/gid 1100、sudo はパスワード無し。公式の isucon-user ロールと同じ)
RUN groupadd -g 1100 isucon \
    && useradd -m -u 1100 -g isucon -s /bin/bash isucon \
    && install -d -m 0755 -o isucon -g isucon /home/isucon \
    && echo 'isucon  ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/99-isucon-user && chmod 0440 /etc/sudoers.d/99-isucon-user \
    && install -d -m 0700 -o isucon -g isucon /home/isucon/.ssh
COPY keys/isucon.pub /home/isucon/.ssh/authorized_keys
RUN chown isucon:isucon /home/isucon/.ssh/authorized_keys && chmod 0600 /home/isucon/.ssh/authorized_keys

# Go 1.21.2(公式の xbuild ロールと同じ版・同じ場所 /home/isucon/local/golang。arm64 版を直接取る)
RUN install -d -o isucon -g isucon /home/isucon/local \
    && curl -fsSL https://go.dev/dl/go1.21.2.linux-arm64.tar.gz | tar -xz -C /tmp \
    && mv /tmp/go /home/isucon/local/golang && chown -R isucon:isucon /home/isucon/local
COPY src/isucon13/provisioning/ansible/roles/xbuild/files/.local.env /home/isucon/.local.env
COPY src/isucon13/provisioning/ansible/roles/xbuild/files/.x /home/isucon/.x
RUN chown isucon:isucon /home/isucon/.local.env /home/isucon/.x && chmod 0755 /home/isucon/.x \
    && echo 'source $HOME/.local.env' >> /home/isucon/.profile

# systemd が読む環境ファイル(公式 templates/env.sh)。ADDRESS は起動のたびに isucon-ip.service が VM の IP に書き換える
RUN printf '%s\n' \
  'ISUCON13_MYSQL_DIALCONFIG_NET="tcp"' \
  'ISUCON13_MYSQL_DIALCONFIG_ADDRESS="127.0.0.1"' \
  'ISUCON13_MYSQL_DIALCONFIG_PORT="3306"' \
  'ISUCON13_MYSQL_DIALCONFIG_USER="isucon"' \
  'ISUCON13_MYSQL_DIALCONFIG_DATABASE="isupipe"' \
  'ISUCON13_MYSQL_DIALCONFIG_PARSETIME="true"' \
  'ISUCON13_POWERDNS_SUBDOMAIN_ADDRESS="127.0.0.1"' \
  'ISUCON13_POWERDNS_DISABLED="false"' > /home/isucon/env.sh \
    && chown isucon:isucon /home/isucon/env.sh && chmod 0755 /home/isucon/env.sh

# webapp(公式は webapp.tar.gz を /home/isucon に展開する。ここでは同じ webapp/ ディレクトリをそのまま置く)
COPY --chown=isucon:isucon src/isucon13/webapp /home/isucon/webapp
RUN su isucon -c 'cd /home/isucon/webapp/go && /home/isucon/local/golang/bin/go build -o /home/isucon/webapp/go/isupipe -ldflags "-s -w"'
COPY src/isucon13/provisioning/ansible/roles/webapp/files/isupipe-go.service /etc/systemd/system/isupipe-go.service

# MySQL / PowerDNS の設定(公式の powerdns ロールと同じファイル)
COPY src/isucon13/provisioning/ansible/roles/powerdns/files/pdns.conf /etc/powerdns/pdns.conf
COPY src/isucon13/provisioning/ansible/roles/powerdns/files/pdns.d/gmysql-host.conf /etc/powerdns/pdns.d/gmysql-host.conf
RUN rm -f /etc/powerdns/pdns.d/bind.conf /etc/powerdns/pdns.d/mysql.conf

# nginx(公式の nginx ロールと同じ設定。証明書だけ自前 CA で署名したものに差し替える)
COPY src/isucon13/provisioning/ansible/roles/nginx/files/etc/nginx/sites-enabled/isupipe.conf /etc/nginx/sites-enabled/isupipe.conf
COPY src/isucon13/provisioning/ansible/roles/nginx/files/etc/nginx/sites-available/isupipe-php.conf /etc/nginx/sites-available/isupipe-php.conf
COPY pki/server.crt /etc/nginx/tls/_.u.isucon.dev.crt
COPY pki/server.key /etc/nginx/tls/_.u.isucon.dev.key
COPY pki/server.crt /etc/nginx/tls/_.t.isucon.dev.crt
COPY pki/server.key /etc/nginx/tls/_.t.isucon.dev.key
COPY pki/ca.crt /etc/nginx/tls/_.u.isucon.dev.issuer.crt
RUN rm -f /etc/nginx/sites-enabled/default /etc/nginx/conf.d/nginx.conf \
    && install -d -o isucon -g isucon /home/isucon/webapp/public \
    && nginx -t

# 自前ファイル(systemd ユニット、sshd、sysctl、PowerDNS のゾーン初期化)
COPY images/server/rootfs/ /
RUN chmod 0755 /opt/isucon-ip.sh /opt/init_zone_once.sh /opt/refresh-zone-ip.sh

# 初期データ入りの MySQL を焼き込む(公式は ansible の webapp ロールでスキーマ作成、初回ベンチの /api/initialize でデータ投入。
# ここでは同じ SQL を先に流して「初期データ投入済み」の状態でイメージにする)
RUN set -eux; \
    mkdir -p /var/run/mysqld && chown mysql:mysql /var/run/mysqld; \
    mysqld --user=mysql --daemonize --skip-networking=0 ; \
    for i in $(seq 1 60); do mysqladmin ping --silent && break; sleep 1; done; \
    mysql -uroot -e "\
      CREATE USER IF NOT EXISTS 'isucon'@'localhost' IDENTIFIED BY 'isucon'; \
      GRANT ALL PRIVILEGES ON *.* TO 'isucon'@'localhost' WITH GRANT OPTION; \
      CREATE USER IF NOT EXISTS 'isudns'@'localhost' IDENTIFIED BY 'isudns'; \
      GRANT ALL PRIVILEGES ON *.* TO 'isudns'@'localhost' WITH GRANT OPTION; \
      FLUSH PRIVILEGES; \
      DROP DATABASE IF EXISTS isupipe; CREATE DATABASE isupipe; \
      DROP DATABASE IF EXISTS isudns;  CREATE DATABASE isudns;"; \
    mysql -uisucon -pisucon < /home/isucon/webapp/sql/initdb.d/10_schema.sql; \
    cd /home/isucon/webapp/sql; \
    for f in init.sql initial_users.sql initial_livestreams.sql initial_tags.sql initial_livestream_tags.sql \
             initial_reservation_slots.sql initial_reactions.sql initial_ngwords.sql initial_livecomments.sql; do \
      mysql -uisucon -pisucon isupipe < $f; \
    done; \
    mysql -uroot isudns < /usr/share/doc/pdns-backend-mysql/schema.mysql.sql; \
    mysql -uroot -e "DROP INDEX nametype_index ON records;" isudns; \
    mysqladmin -uroot shutdown; \
    for i in $(seq 1 60); do pgrep mysqld >/dev/null || break; sleep 1; done

# 有効化するサービス、使わないサービスの無効化
RUN systemctl enable ssh nginx mysql pdns isupipe-go isucon-ip \
    && ln -sf /dev/null /etc/systemd/system/systemd-resolved.service \
    && ssh-keygen -A \
    && rm -f /usr/sbin/policy-rc.d
# 公式の base ロールの pam limits(nofile / nproc / memlock)
RUN printf '%s\n' '* - nofile 655360' '* - nproc 655360' '* soft memlock unlimited' '* hard memlock unlimited' >> /etc/security/limits.conf

STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
