#!/usr/bin/env bash
set -euo pipefail

# Usage: ./scripts/update-aur.sh <version> <checksums-file>
# Example: ./scripts/update-aur.sh v0.1.0 bin/checksums.txt
#
# Requires:
#   - AUR_SSH_PRIVATE_KEY env var containing an ed25519 private key registered
#     to a maintainer account on aur.archlinux.org with push access to the
#     `herd-bin` package.
#   - `makepkg` available on PATH (run on Arch Linux locally, or inside an
#     archlinux:base-devel container in CI).
#   - `git` and `ssh` available on PATH.

VERSION="${1:?Usage: $0 <version> <checksums-file>}"
CHECKSUMS_FILE="${2:?Usage: $0 <version> <checksums-file>}"

# Strip leading 'v' for the PKGBUILD version
PKG_VERSION="${VERSION#v}"

AUR_PACKAGE="herd-bin"
AUR_REMOTE="ssh://aur@aur.archlinux.org/${AUR_PACKAGE}.git"

# Parse checksums
get_sha256() {
	local binary="$1"
	grep " ${binary}\$" "${CHECKSUMS_FILE}" | awk '{print $1}'
}

SHA_LINUX_AMD64=$(get_sha256 "herd-linux-amd64")
SHA_LINUX_ARM64=$(get_sha256 "herd-linux-arm64")

for var in SHA_LINUX_AMD64 SHA_LINUX_ARM64; do
	if [[ -z "${!var}" ]]; then
		echo "ERROR: Could not find checksum for ${var}" >&2
		exit 1
	fi
done

if [[ -z "${AUR_SSH_PRIVATE_KEY:-}" ]]; then
	echo "ERROR: AUR_SSH_PRIVATE_KEY env var is empty" >&2
	exit 1
fi

# Set up ephemeral SSH key + known_hosts
SSH_DIR=$(mktemp -d)
trap 'rm -rf "${SSH_DIR}"' EXIT

printf '%s\n' "${AUR_SSH_PRIVATE_KEY}" > "${SSH_DIR}/id_aur"
chmod 600 "${SSH_DIR}/id_aur"

# AUR's published SSH host key (ed25519). See https://aur.archlinux.org/
cat > "${SSH_DIR}/known_hosts" << 'KNOWN'
aur.archlinux.org ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEuBKrPzbawxA/k2g6NcyV5jmqwJ2s+zpgZGZ7tpLIcN
KNOWN

export GIT_SSH_COMMAND="ssh -i ${SSH_DIR}/id_aur -o IdentitiesOnly=yes -o UserKnownHostsFile=${SSH_DIR}/known_hosts"

# Clone AUR repo
TMPDIR=$(mktemp -d)
trap 'rm -rf "${SSH_DIR}" "${TMPDIR}"' EXIT

git clone "${AUR_REMOTE}" "${TMPDIR}/${AUR_PACKAGE}"
cd "${TMPDIR}/${AUR_PACKAGE}"

# AUR only accepts pushes to `master`. Fresh clones of a not-yet-created
# pkgbase may end up on whatever the local init.defaultBranch is (often `main`),
# so normalize unconditionally — no-op if we're already on master.
git checkout -B master

# Generate PKGBUILD
cat > PKGBUILD << PKGBUILD
# Maintainer: JF Turcot <jf.turcot@gmail.com>
pkgname=herd-bin
_pkgname=herd
pkgver=${PKG_VERSION}
pkgrel=1
pkgdesc="GitHub-native orchestration platform for AI coding agents"
arch=('x86_64' 'aarch64')
url="https://github.com/Herd-OS/herd"
license=('Apache-2.0')
depends=('git' 'github-cli')
optdepends=('docker: self-hosted worker runner containers'
            'docker-compose: legacy docker-compose-based runner deployment')
provides=('herd')
conflicts=('herd' 'herd-git')
options=('!strip' '!debug')
source_x86_64=("herd-\$pkgver-x86_64::https://github.com/Herd-OS/herd/releases/download/v\$pkgver/herd-linux-amd64")
source_aarch64=("herd-\$pkgver-aarch64::https://github.com/Herd-OS/herd/releases/download/v\$pkgver/herd-linux-arm64")
sha256sums_x86_64=('${SHA_LINUX_AMD64}')
sha256sums_aarch64=('${SHA_LINUX_ARM64}')

package() {
  install -Dm755 "herd-\$pkgver-\$CARCH" "\$pkgdir/usr/bin/herd"
}
PKGBUILD

# Generate .SRCINFO (makepkg refuses to run as root; if EUID == 0, drop to a builder user)
if [[ ${EUID} -eq 0 ]]; then
	if ! id -u builder >/dev/null 2>&1; then
		useradd -m builder
	fi
	# mktemp -d creates the parent at 0700 root-owned, which blocks builder from
	# traversing into the clone — makepkg's BUILDDIR writability check then fails
	# with "Failed to create the directory $BUILDDIR" even though the dir exists.
	chmod 755 "${TMPDIR}"
	chown -R builder .
	runuser -u builder -- makepkg --printsrcinfo > .SRCINFO
	chown -R root:root .
	# Cloned worktree may still look "dubious" to git after the chown round-trip
	git config --global --add safe.directory "$PWD"
else
	makepkg --printsrcinfo > .SRCINFO
fi

# Commit and push
git add PKGBUILD .SRCINFO
if git diff --cached --quiet; then
	echo "No changes to commit"
	exit 0
fi

git config user.name "herd-os[bot]"
git config user.email "herd-os[bot]@users.noreply.github.com"
git commit -m "Update to ${VERSION}"
git push
echo "Updated AUR ${AUR_PACKAGE} to ${VERSION}"
