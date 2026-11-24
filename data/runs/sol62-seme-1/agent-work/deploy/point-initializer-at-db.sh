#!/usr/bin/env bash
set -euo pipefail
read -r password
if [[ -e "$HOME/env.sh.original" ]]; then
    cp -p "$HOME/env.sh.original" "$HOME/env.sh"
fi
umask 077
env_next=$(mktemp "$HOME/.isupipe-init-db.next.XXXXXX")
cat > "$env_next" <<ENV
ISUCON13_MYSQL_DIALCONFIG_ADDRESS="10.99.0.13"
ISUCON13_MYSQL_DIALCONFIG_USER="isupipe_initializer"
ISUCON13_MYSQL_DIALCONFIG_PASSWORD="$password"
ENV
mv "$env_next" "$HOME/.isupipe-init-db.env"
sudo install -d -m 755 /etc/systemd/system/isupipe-go.service.d
printf '[Service]\nEnvironmentFile=/home/isucon/.isupipe-init-db.env\n' |
    sudo tee /etc/systemd/system/isupipe-go.service.d/05-initializer-db.conf >/dev/null
sudo systemctl daemon-reload
