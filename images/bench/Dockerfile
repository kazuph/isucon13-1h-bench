# ISUCON13 ベンチマーカー(公式 bench/ のソースを linux/arm64 でビルドし、実行ファイルだけを置く)
FROM golang:1.21.0-bookworm AS build
WORKDIR /src
COPY src/isucon13/bench/ ./
RUN CGO_ENABLED=0 go build -o /out/bench ./cmd/bench

FROM ubuntu:22.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      openssh-server ca-certificates util-linux jq procps iproute2 dnsutils curl \
    && rm -rf /var/lib/apt/lists/*
# 自前 CA を信頼させる(公式の証明書は期限切れで、bench の --enable-ssl は証明書を検証するため)
COPY pki/ca.crt /usr/local/share/ca-certificates/isucon13-1h-bench-ca.crt
RUN update-ca-certificates
RUN useradd -m -s /bin/bash bench && passwd -l bench \
    && install -d -m 0755 /opt/bench /etc/bench /run/sshd /run/bench && chown bench:bench /run/bench
COPY --from=build /out/bench /opt/bench/bench
COPY images/bench/entry.sh /opt/bench/entry.sh
COPY images/common-net.sh /opt/common-net.sh
COPY images/entrypoint.sh /opt/entrypoint.sh
RUN chmod 0755 /opt/bench/bench /opt/bench/entry.sh /opt/common-net.sh /opt/entrypoint.sh
# forced command: この鍵で入ると、何を渡しても entry.sh(ベンチを1回流す)だけが走る
COPY keys/bench_entry.pub /tmp/bench_entry.pub
RUN install -d -m 0700 -o bench -g bench /home/bench/.ssh \
    && printf 'command="/opt/bench/entry.sh",no-pty,no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-user-rc %s\n' "$(cat /tmp/bench_entry.pub)" > /home/bench/.ssh/authorized_keys \
    && chown bench:bench /home/bench/.ssh/authorized_keys && chmod 0600 /home/bench/.ssh/authorized_keys && rm /tmp/bench_entry.pub \
    && ssh-keygen -A \
    && printf 'PasswordAuthentication no\nAllowUsers bench\n' > /etc/ssh/sshd_config.d/99-bench.conf
ENTRYPOINT ["/opt/entrypoint.sh"]
CMD ["/usr/sbin/sshd","-D","-e"]
