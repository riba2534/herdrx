#!/bin/sh
# Install the remote-host CLI from a GitHub Release. Does not manage Herdr.
set -eu
umask 077

herdrx_version=latest
herdrx_install_dir=${HERDRX_INSTALL_DIR:-"${HOME:?HOME is not set}/.local/bin"}
while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) [ "$#" -ge 2 ] || { echo 'Missing --version value' >&2; exit 2; }; herdrx_version=$2; shift 2 ;;
    --install-dir) [ "$#" -ge 2 ] || { echo 'Missing --install-dir value' >&2; exit 2; }; herdrx_install_dir=$2; shift 2 ;;
    -h|--help) echo 'Usage: sh install-herdrx.sh [--version vX.Y.Z] [--install-dir DIR]'; exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 2 ;;
  esac
done

valid_version() { printf '%s\n' "$1" | LC_ALL=C grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$'; }
if [ "$herdrx_version" != latest ] && ! valid_version "$herdrx_version"; then
  echo 'Version must be latest or a release tag such as v0.1.0' >&2; exit 2
fi
case "$(uname -s)" in
  Linux) ;;
  *) echo 'This installer supports Linux. Use SSH access on other remote-host systems.' >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) herdrx_arch=amd64 ;;
  aarch64|arm64) herdrx_arch=arm64 ;;
  *) echo 'Supported CPU architectures: x86_64/amd64 and aarch64/arm64.' >&2; exit 1 ;;
esac
for herdrx_tool in curl tar mktemp install cmp; do
  command -v "$herdrx_tool" >/dev/null 2>&1 || { echo "Please install $herdrx_tool first." >&2; exit 1; }
done
if command -v sha256sum >/dev/null 2>&1; then
  herdrx_hash_tool=sha256sum
elif command -v shasum >/dev/null 2>&1; then
  herdrx_hash_tool=shasum
else
  echo 'Please install sha256sum (coreutils) or shasum before installing.' >&2; exit 1
fi

herdrx_tmp=$(mktemp -d)
herdrx_stage=''
herdrx_backup_stage=''
cleanup() {
  rm -rf "$herdrx_tmp"
  if [ -n "$herdrx_stage" ]; then rm -f "$herdrx_stage"; fi
  if [ -n "$herdrx_backup_stage" ]; then rm -f "$herdrx_backup_stage"; fi
}
trap cleanup 0
trap 'exit 1' HUP INT TERM
herdrx_repo=https://github.com/riba2534/herdrx
if [ "$herdrx_version" = latest ]; then
  herdrx_download="$herdrx_repo/releases/latest/download"
else
  herdrx_download="$herdrx_repo/releases/download/$herdrx_version"
fi
herdrx_archive="herdrx-linux-$herdrx_arch.tar.gz"
download() {
  curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 180 "$herdrx_download/$1" -o "$herdrx_tmp/$1"
}
if ! download "$herdrx_archive" || ! download SHA256SUMS; then
  echo "Download failed. Choose an available version at $herdrx_repo/releases and retry with --version TAG." >&2
  exit 1
fi
herdrx_expected=$(awk -v name="$herdrx_archive" '$2 == name { print $1; count++ } END { if (count != 1) exit 1 }' "$herdrx_tmp/SHA256SUMS") || {
  echo 'The release does not contain exactly one checksum for this archive.' >&2; exit 1
}
if [ "$herdrx_hash_tool" = sha256sum ]; then
  herdrx_actual=$(sha256sum "$herdrx_tmp/$herdrx_archive" | awk '{print $1}')
else
  herdrx_actual=$(shasum -a 256 "$herdrx_tmp/$herdrx_archive" | awk '{print $1}')
fi
if [ "$herdrx_expected" != "$herdrx_actual" ]; then
  echo 'SHA-256 verification failed; the installed CLI has not been changed.' >&2; exit 1
fi
mkdir "$herdrx_tmp/package"
tar -xzf "$herdrx_tmp/$herdrx_archive" -C "$herdrx_tmp/package" herdrx VERSION
for herdrx_member in herdrx VERSION; do
  if [ ! -f "$herdrx_tmp/package/$herdrx_member" ] || [ -L "$herdrx_tmp/package/$herdrx_member" ]; then
    echo "Invalid release member: $herdrx_member" >&2; exit 1
  fi
done
herdrx_package_version=$(cat "$herdrx_tmp/package/VERSION")
if ! valid_version "$herdrx_package_version"; then echo 'Invalid package version.' >&2; exit 1; fi
if [ "$herdrx_version" != latest ] && [ "$herdrx_package_version" != "$herdrx_version" ]; then
  echo 'The package version does not match the requested release.' >&2; exit 1
fi
chmod 755 "$herdrx_tmp/package/herdrx"
if [ "$("$herdrx_tmp/package/herdrx" version)" != "herdrx $herdrx_package_version" ]; then
  echo 'The downloaded CLI did not pass its version check; nothing was installed.' >&2; exit 1
fi
mkdir -p "$herdrx_install_dir"
herdrx_target="$herdrx_install_dir/herdrx"
if [ -d "$herdrx_target" ]; then echo 'The install target is a directory.' >&2; exit 1; fi
if [ -d "$herdrx_install_dir/herdrx.previous" ]; then echo 'The backup target is a directory.' >&2; exit 1; fi
if [ -f "$herdrx_target" ] && cmp -s "$herdrx_tmp/package/herdrx" "$herdrx_target"; then
  echo "herdrx $herdrx_package_version is already installed at $herdrx_target"
else
  herdrx_stage=$(mktemp "$herdrx_install_dir/.herdrx.XXXXXX")
  install -m 755 "$herdrx_tmp/package/herdrx" "$herdrx_stage"
  if [ -f "$herdrx_target" ]; then
    herdrx_backup_stage=$(mktemp "$herdrx_install_dir/.herdrx-backup.XXXXXX")
    install -m 755 "$herdrx_target" "$herdrx_backup_stage"
    mv -f "$herdrx_backup_stage" "$herdrx_install_dir/herdrx.previous"
    herdrx_backup_stage=''
  fi
  mv -f "$herdrx_stage" "$herdrx_target"
  herdrx_stage=''
  echo "Installed herdrx $herdrx_package_version at $herdrx_target (SHA-256 verified)"
fi
printf '\nRun as the same user who runs Herdr:\n'
printf '  export PATH="%s:$PATH"\n' "$herdrx_install_dir"
printf '  herdrx setup\n  herdrx status\n  herdrx connect --plain\n'
printf '\nIf replacing a running CLI, use: herdrx service restart\n'
printf 'Your Herdr installation, tasks, and herdrx configuration were not changed.\n'
