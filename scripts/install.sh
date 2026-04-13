#!/bin/sh
# cockpit install script.
#
# Detects OS + arch, downloads the latest cockpit release tarball from
# GitHub, extracts the binary to $INSTALL_DIR (defaults to ~/.local/bin),
# and prints next steps.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/jeffdhooton/cockpit/main/scripts/install.sh | sh
#
# Customization via env vars:
#   COCKPIT_VERSION   pin to a specific tag (default: latest)
#   INSTALL_DIR       where to drop the binary (default: ~/.local/bin)
#   COCKPIT_REPO      override the GitHub repo (default: jeffdhooton/cockpit)

set -eu

REPO="${COCKPIT_REPO:-jeffdhooton/cockpit}"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${COCKPIT_VERSION:-}"

info()  { printf '\033[1;34mcockpit:\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33mcockpit:\033[0m %s\n' "$*" >&2; }
fail()  { printf '\033[1;31mcockpit:\033[0m %s\n' "$*" >&2; exit 1; }

# ---------- detect platform ----------

uname_s=$(uname -s 2>/dev/null || echo unknown)
uname_m=$(uname -m 2>/dev/null || echo unknown)

case "$uname_s" in
  Darwin)  os=darwin ;;
  Linux)   os=linux ;;
  *)       fail "unsupported OS: $uname_s (cockpit ships for darwin and linux only)" ;;
esac

case "$uname_m" in
  x86_64|amd64)  arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *)             fail "unsupported arch: $uname_m (cockpit ships for amd64 and arm64 only)" ;;
esac

info "detected platform: ${os}_${arch}"

# ---------- resolve version ----------

if [ -z "$VERSION" ]; then
  info "looking up latest release..."
  if ! VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null \
    | grep '"tag_name":' \
    | head -n1 \
    | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/'); then
    fail "failed to query GitHub API for latest release"
  fi
  if [ -z "$VERSION" ]; then
    fail "no 'latest' release found on github.com/${REPO}. Set COCKPIT_VERSION to a specific tag (e.g. v0.1.0)."
  fi
fi
info "installing cockpit ${VERSION}"

# Strip leading 'v' for the archive name template: cockpit_0.1.0_darwin_arm64.tar.gz
version_no_v=${VERSION#v}
archive="cockpit_${version_no_v}_${os}_${arch}.tar.gz"
url="https://github.com/${REPO}/releases/download/${VERSION}/${archive}"

# ---------- download + extract ----------

tmpdir=$(mktemp -d 2>/dev/null || mktemp -d -t cockpit)
trap 'rm -rf "$tmpdir"' EXIT

info "downloading ${url}"
if ! curl -fsSL -o "${tmpdir}/${archive}" "$url"; then
  fail "download failed. Check that ${VERSION} exists at github.com/${REPO}/releases"
fi

# Fetch and verify the checksum.
checksum_file="cockpit_${version_no_v}_checksums.txt"
checksum_url="https://github.com/${REPO}/releases/download/${VERSION}/${checksum_file}"
if curl -fsSL -o "${tmpdir}/${checksum_file}" "$checksum_url" 2>/dev/null; then
  expected=$(grep " ${archive}$" "${tmpdir}/${checksum_file}" | awk '{print $1}')
  if [ -n "$expected" ]; then
    if command -v shasum >/dev/null 2>&1; then
      got=$(shasum -a 256 "${tmpdir}/${archive}" | awk '{print $1}')
    elif command -v sha256sum >/dev/null 2>&1; then
      got=$(sha256sum "${tmpdir}/${archive}" | awk '{print $1}')
    else
      warn "no shasum or sha256sum on PATH — skipping checksum verification"
      got="$expected"
    fi
    if [ "$got" != "$expected" ]; then
      fail "checksum mismatch! expected=$expected got=$got"
    fi
    info "checksum verified"
  else
    warn "couldn't find ${archive} in checksum file — skipping verification"
  fi
else
  warn "couldn't download checksum file — skipping verification"
fi

info "extracting..."
tar -xzf "${tmpdir}/${archive}" -C "$tmpdir"

if [ ! -f "${tmpdir}/cockpit" ]; then
  fail "extracted archive did not contain a 'cockpit' binary"
fi

# ---------- install ----------

mkdir -p "$INSTALL_DIR"
install -m 0755 "${tmpdir}/cockpit" "${INSTALL_DIR}/cockpit"

info "installed to ${INSTALL_DIR}/cockpit"

# ---------- PATH advisory ----------

case ":$PATH:" in
  *":${INSTALL_DIR}:"*) : ;;
  *)
    warn "${INSTALL_DIR} is not on your PATH. Add it with:"
    printf '\n  export PATH="%s:$PATH"\n\n' "$INSTALL_DIR" >&2
    warn "(then open a new shell or source your rc file)"
    ;;
esac

cat <<EOF

Next steps:

  1. Verify the install:
       cockpit version

  2. Run it:
       cockpit

Full docs: https://github.com/${REPO}
EOF
