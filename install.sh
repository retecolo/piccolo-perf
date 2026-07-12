#!/bin/sh
# TinyTWAMP installer
# Usage: /bin/sh -c "$(curl -fsSL https://raw.githubusercontent.com/buraglio/tiny-twamp/main/install.sh)"
#
# Installs the latest tinytwamp release binary to /usr/local/bin (or ~/bin if not writable).
# Supports Linux, macOS, FreeBSD, OpenBSD, NetBSD, DragonFly BSD, Solaris.

set -e

REPO="buraglio/tiny-twamp"
BINARY="tinytwamp"
INSTALL_DIR="/usr/local/bin"

# ── Helpers ────────────────────────────────────────────────────────────────────

info()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()    { printf '\033[1;32m  ✓\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m  !\033[0m %s\n' "$*"; }
die()   { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }

need() {
    command -v "$1" >/dev/null 2>&1 || die "Required tool not found: $1"
}

# ── Detect OS and architecture ─────────────────────────────────────────────────

detect_os() {
    OS="$(uname -s)"
    case "$OS" in
        Linux)   echo "linux" ;;
        Darwin)  echo "darwin" ;;
        FreeBSD) echo "freebsd" ;;
        OpenBSD) echo "openbsd" ;;
        NetBSD)  echo "netbsd" ;;
        DragonFly) echo "dragonfly" ;;
        SunOS)   echo "solaris" ;;
        *)       die "Unsupported OS: $OS" ;;
    esac
}

detect_arch() {
    ARCH="$(uname -m)"
    case "$ARCH" in
        x86_64|amd64)   echo "amd64" ;;
        arm64|aarch64)  echo "arm64" ;;
        armv6*|armv7*)  echo "arm" ;;
        i386|i686)      echo "386" ;;
        mips)
            # Detect endianness
            if echo I | od -L -i 2>/dev/null | grep -q "0000001"; then
                echo "mipsle"
            else
                echo "mips"
            fi
            ;;
        mips64)         echo "mips64" ;;
        mips64el)       echo "mips64le" ;;
        ppc64le)        echo "ppc64le" ;;
        riscv64)        echo "riscv64" ;;
        s390x)          echo "s390x" ;;
        *)              die "Unsupported architecture: $ARCH" ;;
    esac
}

# ── Fetch latest release version from GitHub ──────────────────────────────────

latest_version() {
    curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' \
        | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/'
}

# ── Main ───────────────────────────────────────────────────────────────────────

main() {
    need curl
    need tar

    OS="$(detect_os)"
    ARCH="$(detect_arch)"

    info "Detecting platform: ${OS}/${ARCH}"

    VERSION="$(latest_version)"
    [ -n "$VERSION" ] || die "Could not determine latest release version"
    info "Latest release: ${VERSION}"

    # Strip leading 'v' for the filename (GoReleaser uses the version number without 'v')
    VER="${VERSION#v}"

    ASSET="${BINARY}_${VER}_${OS}_${ARCH}.tar.gz"
    URL="https://github.com/${REPO}/releases/download/${VERSION}/${ASSET}"
    CHECKSUM_URL="https://github.com/${REPO}/releases/download/${VERSION}/checksums.txt"

    info "Downloading ${ASSET}"
    TMPDIR="$(mktemp -d)"
    trap 'rm -rf "$TMPDIR"' EXIT

    curl -fsSL "$URL" -o "${TMPDIR}/${ASSET}" || die "Download failed: ${URL}"
    curl -fsSL "$CHECKSUM_URL" -o "${TMPDIR}/checksums.txt" || die "Download failed: ${CHECKSUM_URL}"

    # Verify checksum
    if command -v sha256sum >/dev/null 2>&1; then
        info "Verifying checksum (sha256sum)"
        cd "$TMPDIR"
        grep "${ASSET}" checksums.txt | sha256sum -c - || die "Checksum verification failed"
        cd - >/dev/null
    elif command -v shasum >/dev/null 2>&1; then
        info "Verifying checksum (shasum)"
        cd "$TMPDIR"
        grep "${ASSET}" checksums.txt | shasum -a 256 -c - || die "Checksum verification failed"
        cd - >/dev/null
    else
        warn "No sha256 tool found — skipping checksum verification"
    fi
    ok "Checksum verified"

    # Extract
    tar -xzf "${TMPDIR}/${ASSET}" -C "$TMPDIR"

    # Determine install location
    if [ -w "$INSTALL_DIR" ]; then
        DEST="${INSTALL_DIR}/${BINARY}"
    elif [ "$(id -u)" -eq 0 ]; then
        DEST="${INSTALL_DIR}/${BINARY}"
    else
        # Try sudo
        if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
            DEST="${INSTALL_DIR}/${BINARY}"
            USE_SUDO=1
        else
            warn "${INSTALL_DIR} is not writable; installing to ~/bin instead"
            mkdir -p "$HOME/bin"
            DEST="${HOME}/bin/${BINARY}"
        fi
    fi

    info "Installing to ${DEST}"
    if [ "${USE_SUDO:-0}" = "1" ]; then
        sudo install -m 755 "${TMPDIR}/${BINARY}" "$DEST"
    else
        install -m 755 "${TMPDIR}/${BINARY}" "$DEST"
    fi

    ok "Installed ${BINARY} ${VERSION} to ${DEST}"

    # Warn if ~/bin is not in PATH
    if [ "$DEST" = "${HOME}/bin/${BINARY}" ]; then
        case ":${PATH}:" in
            *":${HOME}/bin:"*) ;;
            *) warn "Add ~/bin to your PATH: export PATH=\"\$HOME/bin:\$PATH\"" ;;
        esac
    fi

    info "Verifying installation"
    "$DEST" -version
    ok "Done. Run '${BINARY} --help' to get started."
}

main "$@"
