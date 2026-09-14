#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
cleanup() {
    rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

mkdir -p "$tmp_dir/bin" "$tmp_dir/root"
printf '%s\n' '#!/bin/sh' 'printf "0\n"' >"$tmp_dir/bin/id"
printf '%s\n' '#!/bin/sh' 'printf "%s\n" "$*" >>"$WWR_TEST_SYSTEMCTL_LOG"' >"$tmp_dir/bin/systemctl"
printf '%s\n' '#!/bin/sh' 'exit 0' >"$tmp_dir/bin/tun2socks"
chmod 755 "$tmp_dir/bin/id" "$tmp_dir/bin/systemctl" "$tmp_dir/bin/tun2socks"

export PATH="$tmp_dir/bin:/usr/bin:/bin"
export WWR_INSTALL_ROOT="$tmp_dir/root"
export WWR_TUN2SOCKS_BIN="$tmp_dir/bin/tun2socks"
export WWR_TEST_SYSTEMCTL_LOG="$tmp_dir/systemctl.log"
export WWR_PROXY_STATUS_UID=1000

"$repo_dir/scripts/install-transparent-service.sh" >"$tmp_dir/install.log" 2>&1
libexec=$tmp_dir/root/usr/local/libexec/wsl-win-relay
config=$tmp_dir/root/etc/wsl-win-relay/transparent.env
unit=$tmp_dir/root/etc/systemd/system/wsl-win-relay-transparent.service
[ -x "$libexec/transparent-relay.sh" ]
[ -x "$libexec/tun2socks" ]
[ -f "$unit" ]
[ "$(stat -c '%a' "$tmp_dir/root/etc/wsl-win-relay")" = 700 ]
[ "$(stat -c '%a' "$config")" = 600 ]
grep -q '^ExecStart=/usr/local/libexec/wsl-win-relay/transparent-relay.sh$' "$unit"
grep -q '^WWR_TUN2SOCKS_BIN=/usr/local/libexec/wsl-win-relay/tun2socks$' "$config"
grep -q '^WWR_PROXY_STATUS_UID=1000$' "$config"
grep -qx 'daemon-reload' "$WWR_TEST_SYSTEMCTL_LOG"
grep -qx 'enable wsl-win-relay-transparent.service' "$WWR_TEST_SYSTEMCTL_LOG"
grep -qx 'restart wsl-win-relay-transparent.service' "$WWR_TEST_SYSTEMCTL_LOG"

printf '%s\n' 'WWR_DNS=203.0.113.53' >>"$config"
"$repo_dir/scripts/install-transparent-service.sh" >"$tmp_dir/reinstall.log" 2>&1
[ "$(grep -c '^WWR_DNS=203.0.113.53$' "$config")" -eq 1 ]

mv "$config" "$tmp_dir/transparent.env.saved"
ln -s "$tmp_dir/transparent.env.saved" "$config"
set +e
"$repo_dir/scripts/install-transparent-service.sh" >"$tmp_dir/symlink.log" 2>&1
symlink_status=$?
set -e
[ "$symlink_status" -ne 0 ]
grep -q 'refusing symlinked installation target' "$tmp_dir/symlink.log"
rm "$config"
mv "$tmp_dir/transparent.env.saved" "$config"

"$repo_dir/scripts/install-transparent-service.sh" --uninstall >"$tmp_dir/uninstall.log" 2>&1
[ ! -e "$libexec/transparent-relay.sh" ]
[ ! -e "$libexec/tun2socks" ]
[ ! -e "$unit" ]
[ -f "$config" ]
grep -qx 'disable --now wsl-win-relay-transparent.service' "$WWR_TEST_SYSTEMCTL_LOG"
grep -q 'preserved .*transparent.env' "$tmp_dir/uninstall.log"
"$repo_dir/scripts/install-transparent-service.sh" --uninstall >"$tmp_dir/uninstall-again.log" 2>&1
[ -f "$config" ]

echo "transparent system service installer is idempotent and preserves private configuration"
