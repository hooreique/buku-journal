#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'HELP'
Usage: buku-journal-deploy [--domain DOMAIN] [--port PORT] [--journal-dir PATH]

Install or update the user service and the ordinary hooddy site, then reload
Caddy. Requires an installed hooddy service and an active systemd user session.
Defaults: buku-journal.hooreique.com, port 3002, XDG data journal directory.
Run as the user who owns hooddy. Existing journal files are never imported or
removed by deployment. The application creates its private access key.
HELP
}
die() { printf 'buku-journal-deploy: %s\n' "$*" >&2; exit 1; }

config_home=${XDG_CONFIG_HOME:-$HOME/.config}
data_home=${XDG_DATA_HOME:-$HOME/.local/share}
state_home=${XDG_STATE_HOME:-$HOME/.local/state}
domain=buku-journal.hooreique.com
port=3002
journal_dir=$data_home/buku-journal/journal
while (($#)); do
  case "$1" in
    --help|-h) usage; exit 0 ;;
    --domain|--port|--journal-dir)
      (($# >= 2)) || die "missing value for $1"
      case "$1" in
        --domain) domain=$2 ;;
        --port) port=$2 ;;
        --journal-dir) journal_dir=$2 ;;
      esac
      shift 2 ;;
    *) die "unknown option: $1" ;;
  esac
done
[[ $domain =~ ^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?$ && $domain == *.* ]] || die 'expected a DNS domain without protocol or port'
[[ $port =~ ^[0-9]{1,5}$ ]] || die 'invalid port'
port=$((10#$port))
((port >= 1024 && port <= 65535)) || die 'port must be between 1024 and 65535'
for path in "$config_home" "$data_home" "$state_home" "$journal_dir"; do
  [[ $path == /* && $path != *$'\n'* && $path != *$'\r'* ]] || die 'all paths must be absolute and single-line'
done

app_config=$config_home/buku-journal
app_state=$state_home/buku-journal
unit_dir=$config_home/systemd/user
unit=$unit_dir/buku-journal.service
site_dir=$config_home/hooddy/sites.d
site=$site_dir/buku-journal.caddy
master=$config_home/hooddy/Caddyfile
caddy=$config_home/hooddy/caddy/bin/caddy
package_root=$app_state/package
marker='# Managed by buku-journal v1'

systemctl --user show-environment >/dev/null || die 'systemd user session unavailable'
[[ -x $caddy && -f $master ]] || die 'install hooddy before deploying'
[[ $(systemctl --user show hooddy.service --property=FragmentPath --value) ]] || die 'hooddy service is not installed'
[[ $(loginctl show-user "$(id -un)" --property=Linger --value) == yes ]] || die 'enable lingering with loginctl enable-linger before deploying'
for file in "$unit" "$site"; do
  if [[ -e $file || -L $file ]]; then
    [[ -f $file && ! -L $file && $(head -n 1 -- "$file") == "$marker" ]] || die "refusing to replace an unmanaged file: $file"
  fi
done
if [[ -e $package_root || -L $package_root ]]; then
  [[ -L $package_root && $(readlink -f -- "$package_root") == /nix/store/* ]] || die 'package GC root is not a Nix store link'
fi

umask 077
mkdir -p -- "$app_config" "$app_state" "$journal_dir" "$unit_dir" "$site_dir"
exec 9>"$app_state/deploy.lock"
flock 9
# Match hooddy's lock so a concurrent reload never sees a half-applied deployment.
exec 8>"$config_home/hooddy/.lock"
flock 8
backup=$(mktemp -d "$app_state/.deploy.XXXXXX")
had_unit=0
had_site=0
had_root=0
was_active=0
was_enabled=0
changed=0
complete=0
[[ ! -f $unit ]] || { cp -- "$unit" "$backup/unit"; had_unit=1; }
[[ ! -f $site ]] || { cp -- "$site" "$backup/site"; had_site=1; }
[[ ! -L $package_root ]] || { old_package=$(readlink -f -- "$package_root"); had_root=1; }
systemctl --user is-active --quiet buku-journal.service && was_active=1
systemctl --user is-enabled --quiet buku-journal.service 2>/dev/null && was_enabled=1

# shellcheck disable=SC2329
cleanup() {
  local status=$?
  trap - EXIT
  if ((changed && !complete)); then
    printf 'Deployment failed; restoring the previous service and site.\n' >&2
    systemctl --user stop buku-journal.service >/dev/null 2>&1 || true
    if ((had_unit)); then cp -- "$backup/unit" "$unit"; else rm -f -- "$unit"; fi
    if ((had_site)); then cp -- "$backup/site" "$site"; else rm -f -- "$site"; fi
    systemctl --user daemon-reload >/dev/null 2>&1 || true
    if ((was_enabled)); then systemctl --user enable buku-journal.service >/dev/null 2>&1 || true; else systemctl --user disable buku-journal.service >/dev/null 2>&1 || true; fi
    if ((was_active)); then systemctl --user start buku-journal.service >/dev/null 2>&1 || true; fi
    if ((had_root)); then
      nix-store --add-root "$package_root" --indirect -r "$old_package" >/dev/null 2>&1 || true
    else
      rm -f -- "$package_root"
    fi
    "$caddy" validate --config "$master" --adapter caddyfile >/dev/null 2>&1 && systemctl --user reload-or-restart hooddy.service >/dev/null 2>&1 || true
  fi
  rm -rf -- "$backup"
  exit "$status"
}
trap cleanup EXIT

unit_string() {
  local value=$1
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//%/%%}
  printf '"%s"' "$value"
}
unit_argument() {
  local value=$1
  value=${value//\$/\$\$}
  unit_string "$value"
}

cat >"$backup/unit.next" <<UNIT
$marker
[Unit]
Description=Buku Journal — our private journal
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment=$(unit_string "XDG_CONFIG_HOME=$config_home")
Environment=$(unit_string "XDG_DATA_HOME=$data_home")
Environment=$(unit_string "XDG_STATE_HOME=$state_home")
Environment=$(unit_string "CODEX_HOME=${CODEX_HOME:-$HOME/.codex}")
Environment="XDG_RUNTIME_DIR=%t"
ExecStart=@APP@/bin/buku-journal serve --listen 127.0.0.1:$port --journal-dir $(unit_argument "$journal_dir") --state-dir $(unit_argument "$app_state") --token-file $(unit_argument "$app_config/token")
Restart=on-failure
RestartSec=3s
TimeoutStopSec=30s
KillMode=mixed
UMask=0077

[Install]
WantedBy=default.target
UNIT
cat >"$backup/site.next" <<SITE
$marker
$domain {
    header X-Robots-Tag "noindex, nofollow"
    header Referrer-Policy "no-referrer"
    reverse_proxy 127.0.0.1:$port
}
SITE

# Retain both versions until the new service and site have passed validation.
nix-store --add-root "$backup/package" --indirect -r @APP@ >/dev/null
changed=1
install -m 600 "$backup/unit.next" "$unit_dir/.buku-journal.service.pending"
mv -f -- "$unit_dir/.buku-journal.service.pending" "$unit"
systemctl --user daemon-reload
systemctl --user enable buku-journal.service >/dev/null
systemctl --user restart buku-journal.service
healthy=0
for ((attempt=0; attempt<30; attempt++)); do
  if curl --fail --silent --max-time 2 "http://127.0.0.1:$port/healthz" >/dev/null; then healthy=1; break; fi
  sleep 1
done
((healthy)) || die 'backend health check failed; inspect journalctl --user -u buku-journal.service'
systemctl --user is-active --quiet buku-journal.service || die 'backend exited during startup'
install -m 600 "$backup/site.next" "$site_dir/.buku-journal.caddy.pending"
mv -f -- "$site_dir/.buku-journal.caddy.pending" "$site"
"$caddy" validate --config "$master" --adapter caddyfile >/dev/null
# This is the same service reload performed by hooddy restart; no hooddy CLI
# dependency or application registration metadata is required.
systemctl --user reload-or-restart hooddy.service
nix-store --add-root "$package_root" --indirect -r @APP@ >/dev/null
complete=1
printf 'Deployed: https://%s\n' "$domain"
printf 'Journal: %s\n' "$journal_dir"
printf 'Access key file: %s/token (read locally; never publish it)\n' "$app_config"
