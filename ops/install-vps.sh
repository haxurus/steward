#!/bin/sh
# One-time (and idempotent) installation of the YAGPDB production infrastructure on
# VPS01. Run from a checkout of this repository: sudo ./ops/install-vps.sh KEY.pub
# See docs/DEPLOYMENT.md, section 2.
set -eu

PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
SCRIPT_DIR=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
REPO_DIR=$(CDPATH='' cd -- "$SCRIPT_DIR/.." && pwd)
STATE_DIR=/srv/docker/yagpdb
APP_SECRETS="$STATE_DIR/secrets/app"
DEPLOY_USER=yagpdb-deploy
# uid/gid of the yagpdb user inside the image (deploy/Dockerfile).
APP_GID=10001
KEY_FILE=${1:-}

log() { printf '[yagpdb-install] %s\n' "$*"; }
die() { printf '[yagpdb-install] ERROR: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die 'run with sudo/root'
command -v docker >/dev/null 2>&1 || die 'Docker is required'
command -v openssl >/dev/null 2>&1 || die 'OpenSSL is required'
command -v visudo >/dev/null 2>&1 || die 'sudo/visudo is required'
command -v flock >/dev/null 2>&1 || die 'flock (util-linux) is required'
docker compose version >/dev/null 2>&1 || die 'Docker Compose plugin is required'
docker network inspect proxy_net >/dev/null 2>&1 || die 'Docker network proxy_net does not exist'

if command -v sshd >/dev/null 2>&1; then
  allow_users=$(sshd -T 2>/dev/null | awk '$1 == "allowusers" { for (i=2; i<=NF; i++) print $i }' || true)
  if [ -n "$allow_users" ] && ! printf '%s\n' "$allow_users" | grep -Fxq "$DEPLOY_USER"; then
    die "sshd AllowUsers is active but does not include $DEPLOY_USER"
  fi
fi

deploy_key=''
if [ -n "$KEY_FILE" ]; then
  [ -f "$KEY_FILE" ] || die 'deploy public key file not found'
  # Exactly one key: extra lines would be installed without the forced command.
  [ "$(grep -c '' "$KEY_FILE")" -eq 1 ] || die 'deploy public key file must contain exactly one key'
  deploy_key=$(cat "$KEY_FILE")
  printf '%s\n' "$deploy_key" | grep -Eq '^ssh-ed25519 [A-Za-z0-9+/=]+' || die 'only an Ed25519 deploy public key is accepted'
fi

if ! id "$DEPLOY_USER" >/dev/null 2>&1; then
  useradd --system --create-home --home-dir /var/lib/yagpdb-deploy --shell /bin/sh "$DEPLOY_USER"
fi

install -d -o root -g root -m 700 "$STATE_DIR" "$STATE_DIR/secrets" "$STATE_DIR/backups" "$STATE_DIR/logs"
install -d -o root -g root -m 755 "$STATE_DIR/runtime"
install -d -o root -g "$APP_GID" -m 750 "$APP_SECRETS"

install -o root -g root -m 755 "$REPO_DIR/ops/yagpdb-deploy" /usr/local/sbin/yagpdb-deploy
install -d -o root -g root -m 755 /usr/local/libexec
install -o root -g root -m 755 "$REPO_DIR/ops/yagpdb-deploy-entrypoint" /usr/local/libexec/yagpdb-deploy-entrypoint
install -o root -g root -m 755 "$REPO_DIR/security/host-firewall.sh" /usr/local/sbin/yagpdb-egress-firewall.sh

if [ ! -f "$STATE_DIR/yagpdb.env" ]; then
  install -o root -g root -m 600 "$REPO_DIR/deploy/yagpdb.env.example" "$STATE_DIR/yagpdb.env"
  log "created $STATE_DIR/yagpdb.env from template"
fi

umask 077
secret_if_missing() {
  file="$1"
  if [ ! -s "$file" ]; then
    openssl rand -hex 32 | tr -d '\n' > "$file"
  fi
}

secret_if_missing "$STATE_DIR/secrets/postgres_admin_password"
chown root:root "$STATE_DIR/secrets/postgres_admin_password"
chmod 600 "$STATE_DIR/secrets/postgres_admin_password"

secret_if_missing "$APP_SECRETS/YAGPDB_PQPASSWORD"
secret_if_missing "$APP_SECRETS/YAGPDB_YOUTUBE_VERIFY_TOKEN"
# Filled by hand (section 3); empty files are ignored by the container entrypoint.
for name in YAGPDB_BOTTOKEN YAGPDB_CLIENTSECRET; do
  [ -e "$APP_SECRETS/$name" ] || : > "$APP_SECRETS/$name"
done
find "$APP_SECRETS" -maxdepth 1 -type f -exec chown "root:$APP_GID" {} + -exec chmod 640 {} +

home=$(getent passwd "$DEPLOY_USER" | cut -d: -f6)
# Home, .ssh and authorized_keys are root-owned: the deploy account cannot
# strip the forced command or plant symlinks that root would write through.
for path in "$home" "$home/.ssh" "$home/.ssh/authorized_keys"; do
  [ ! -L "$path" ] || die "refusing to use symlink $path"
done
chown root:root "$home"
chmod 755 "$home"
install -d -o root -g root -m 755 "$home/.ssh"
if [ -n "$deploy_key" ]; then
  tmp_keys="$home/.ssh/authorized_keys.tmp.$$"
  printf 'restrict,command="/usr/local/libexec/yagpdb-deploy-entrypoint" %s\n' "$deploy_key" > "$tmp_keys"
  chmod 644 "$tmp_keys"
  mv -f "$tmp_keys" "$home/.ssh/authorized_keys"
elif [ ! -s "$home/.ssh/authorized_keys" ]; then
  die 'first install requires the deploy public key'
else
  chown root:root "$home/.ssh/authorized_keys"
  chmod 644 "$home/.ssh/authorized_keys"
fi

cat > /etc/sudoers.d/yagpdb-deploy <<'SUDOEOF'
Defaults:yagpdb-deploy !setenv
yagpdb-deploy ALL=(root) NOPASSWD: /usr/local/sbin/yagpdb-deploy *
SUDOEOF
chmod 440 /etc/sudoers.d/yagpdb-deploy
visudo -cf /etc/sudoers.d/yagpdb-deploy >/dev/null

cat > /etc/systemd/system/yagpdb-firewall.service <<'UNITEOF'
[Unit]
Description=YAGPDB Docker egress firewall
Requires=docker.service
After=docker.service docker-firewall.service

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/yagpdb-egress-firewall.sh
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
UNITEOF

systemctl daemon-reload
systemctl enable yagpdb-firewall.service >/dev/null
systemctl restart yagpdb-firewall.service

log 'installation complete'
log "edit $STATE_DIR/yagpdb.env (sudoedit)"
log "fill $APP_SECRETS/YAGPDB_BOTTOKEN and YAGPDB_CLIENTSECRET (sudoedit)"
log 'configure Nginx Proxy Manager to forward yagpdb.haxurus.com to yagpdb-edge:8080'
