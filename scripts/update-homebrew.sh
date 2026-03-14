#!/usr/bin/env bash
set -euo pipefail

# Usage: ./scripts/update-homebrew.sh <version> <checksums-file>
# Example: ./scripts/update-homebrew.sh v0.1.0 bin/checksums.txt
#
# Requires HERD_GITHUB_TOKEN to be set for pushing to the tap repo.

VERSION="${1:?Usage: $0 <version> <checksums-file>}"
CHECKSUMS_FILE="${2:?Usage: $0 <version> <checksums-file>}"

# Strip leading 'v' for the formula version
FORMULA_VERSION="${VERSION#v}"

TAP_REPO="Herd-OS/homebrew-tap"
DOWNLOAD_BASE="https://github.com/Herd-OS/herd/releases/download/${VERSION}"

# Parse checksums
get_sha256() {
	local binary="$1"
	grep "${binary}" "${CHECKSUMS_FILE}" | awk '{print $1}'
}

SHA_DARWIN_AMD64=$(get_sha256 "herd-darwin-amd64")
SHA_DARWIN_ARM64=$(get_sha256 "herd-darwin-arm64")
SHA_LINUX_AMD64=$(get_sha256 "herd-linux-amd64")
SHA_LINUX_ARM64=$(get_sha256 "herd-linux-arm64")

# Validate all checksums were found
for var in SHA_DARWIN_AMD64 SHA_DARWIN_ARM64 SHA_LINUX_AMD64 SHA_LINUX_ARM64; do
	if [[ -z "${!var}" ]]; then
		echo "ERROR: Could not find checksum for ${var}" >&2
		exit 1
	fi
done

# Clone tap repo
TMPDIR=$(mktemp -d)
trap 'rm -rf "${TMPDIR}"' EXIT

git clone "https://x-access-token:${HERD_GITHUB_TOKEN}@github.com/${TAP_REPO}.git" "${TMPDIR}/tap"
cd "${TMPDIR}/tap"

# Generate formula
mkdir -p Formula
cat > Formula/herd.rb << RUBY
class Herd < Formula
  desc "GitHub-native orchestration for agentic development systems"
  homepage "https://github.com/Herd-OS/herd"
  version "${FORMULA_VERSION}"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "${DOWNLOAD_BASE}/herd-darwin-arm64"
      sha256 "${SHA_DARWIN_ARM64}"
    else
      url "${DOWNLOAD_BASE}/herd-darwin-amd64"
      sha256 "${SHA_DARWIN_AMD64}"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "${DOWNLOAD_BASE}/herd-linux-arm64"
      sha256 "${SHA_LINUX_ARM64}"
    else
      url "${DOWNLOAD_BASE}/herd-linux-amd64"
      sha256 "${SHA_LINUX_AMD64}"
    end
  end

  def install
    bin.install Dir["herd-*"].first => "herd"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/herd version")
  end
end
RUBY

# Commit and push
git add Formula/herd.rb
git diff --cached --quiet && echo "No changes to commit" && exit 0
git config user.name "herd-os[bot]"
git config user.email "herd-os[bot]@users.noreply.github.com"
git commit -m "Update herd to ${VERSION}"
git push
echo "Updated homebrew-tap to ${VERSION}"
