# AI(Claude Code / Codex CLI)を走らせる VM。競技サーバーの外から ssh で s1〜s3 に入り、bench コマンドでベンチを打つ。
FROM ubuntu:22.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl xz-utils git openssh-client jq mysql-client iproute2 iputils-ping dnsutils procps less vim rsync unzip build-essential python3 \
    && rm -rf /var/lib/apt/lists/*
# Node.js 22(arm64)
RUN curl -fsSL "https://nodejs.org/dist/latest-v22.x/$(curl -fsSL https://nodejs.org/dist/latest-v22.x/ | grep -o 'node-v22[^"]*linux-arm64.tar.xz' | head -1)" | tar -xJ -C /usr/local --strip-components=1
RUN npm install -g @anthropic-ai/claude-code @openai/codex \
    && curl -fsSL https://antigravity.google/cli/install.sh | bash -s -- -d /usr/local/bin \
    && { echo "node $(node --version)"; echo "claude-code $(claude --version)"; echo "codex $(codex --version)"; echo "agy $(agy --version)"; } > /etc/agent-versions.txt \
    && cat /etc/agent-versions.txt
RUN groupadd -g 1100 isucon && useradd -m -u 1100 -g isucon -s /bin/bash isucon
COPY --chown=isucon:isucon keys/isucon /home/isucon/.ssh/id_isucon
COPY --chown=isucon:isucon keys/bench_entry /home/isucon/.ssh/id_bench_entry
RUN printf 'Host s1 s2 s3\n  User isucon\n  IdentityFile ~/.ssh/id_isucon\n  IdentitiesOnly yes\n  StrictHostKeyChecking no\n  UserKnownHostsFile /dev/null\n  LogLevel ERROR\n' > /home/isucon/.ssh/config \
    && chown isucon:isucon /home/isucon/.ssh/config && chmod 700 /home/isucon/.ssh && chmod 600 /home/isucon/.ssh/*
COPY images/agent/bench /usr/local/bin/bench
COPY images/common-net.sh /opt/common-net.sh
COPY images/entrypoint.sh /opt/entrypoint.sh
RUN chmod 0755 /usr/local/bin/bench /opt/common-net.sh /opt/entrypoint.sh \
    && install -d -o isucon -g isucon /home/isucon/work /home/isucon/work/manual
COPY --chown=isucon:isucon src/isucon13/docs/cautionary_note.md src/isucon13/docs/isupipe.md /home/isucon/work/manual/
ENTRYPOINT ["/opt/entrypoint.sh"]
CMD ["sleep","infinity"]
