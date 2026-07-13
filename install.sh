#!/bin/sh
set -eu

REPO="harness/harness-unified-cli"
INSTALL_DIR="${HARNESS_INSTALL_DIR:-$HOME/.local/bin}"
USER_OVERRIDE="${HARNESS_INSTALL_DIR:+yes}"
NONINTERACTIVE="${HARNESS_NONINTERACTIVE:-}"
NO_VERIFY="${HARNESS_NO_VERIFY:-}"
CORE_ONLY="${HARNESS_CORE_ONLY:-}"

# ── helpers ────────────────────────────────────────────────────────────────────

info()    { printf '  \033[34m•\033[0m %s\n' "$*"; }
success() { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn()    { printf '  \033[33m!\033[0m %s\n' "$*"; }
error()   { printf '  \033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }

is_interactive() {
    [ -n "$NONINTERACTIVE" ] && return 1
    { true </dev/tty; } 2>/dev/null
}

confirm() {
    local prompt="$1"
    local answer
    printf '  \033[34m?\033[0m %s [Y/n] ' "$prompt"
    read -r answer </dev/tty
    case "$answer" in
        [nN]*) return 1 ;;
        *)     return 0 ;;
    esac
}

# ── argument parsing ───────────────────────────────────────────────────────────

parse_args() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --non-interactive) NONINTERACTIVE=1                      ; shift ;;
            --no-verify)       NO_VERIFY=1                           ; shift ;;
            --core)            CORE_ONLY=1                           ; shift ;;
            --install-dir)     INSTALL_DIR="$2" ; USER_OVERRIDE=yes ; shift 2 ;;
            --install-dir=*)   INSTALL_DIR="${1#*=}" ; USER_OVERRIDE=yes ; shift ;;
            *) error "Unknown option: $1" ;;
        esac
    done
}

# ── platform detection ─────────────────────────────────────────────────────────

detect_platform() {
    local os arch

    case "$(uname -s)" in
        Darwin) os="darwin" ;;
        Linux)  os="linux"  ;;
        *)      error "Unsupported OS: $(uname -s)" ;;
    esac

    case "$(uname -m)" in
        x86_64)          arch="amd64" ;;
        arm64 | aarch64) arch="arm64" ;;
        *)               error "Unsupported architecture: $(uname -m)" ;;
    esac

    echo "${os}_${arch}"
}

detect_shell_rc() {
    local shell_name
    shell_name="$(basename "${SHELL:-bash}")"
    case "$shell_name" in
        zsh)  echo "$HOME/.zshrc"    ;;
        bash) echo "$HOME/.bashrc"   ;;
        fish) echo "$HOME/.config/fish/config.fish" ;;
        *)    echo "$HOME/.bashrc"   ;;  # safe fallback
    esac
}

# ── download ───────────────────────────────────────────────────────────────────

latest_version() {
    curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
        | grep '"tag_name"' \
        | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/'
}

sha256_file() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | cut -d' ' -f1
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$1" | cut -d' ' -f1
    else
        error "No sha256sum or shasum command found — cannot verify download. Pass --no-verify to skip verification."
    fi
}

download_and_install() {
    local version="$1"
    local platform="$2"
    local dest="$3"
    local ver="${version#v}"
    local pkg_name
    local binaries

    if [ -n "$CORE_ONLY" ]; then
        pkg_name="harness-core_${ver}_${platform}"
        binaries="harness"
    else
        pkg_name="harness-bundle_${ver}_${platform}"
        binaries="harness harness-har"
    fi

    local url="https://github.com/${REPO}/releases/download/${version}/${pkg_name}.tar.gz"
    local checksum_url="https://github.com/${REPO}/releases/download/${version}/harness_${ver}_checksums.txt"
    local tmp
    tmp="$(mktemp -d)"
    [ -z "$tmp" ] && error "Failed to create temporary directory"

    info "Downloading ${pkg_name} $version ($platform)..."
    curl -fsSL "$url" -o "$tmp/harness.tar.gz"

    if [ -n "$NO_VERIFY" ]; then
        warn "Skipping checksum verification (--no-verify)"
    else
        info "Verifying checksum..."
        curl -fsSL "$checksum_url" -o "$tmp/checksums.txt"
        local expected actual
        expected="$(grep "${pkg_name}.tar.gz" "$tmp/checksums.txt" | cut -d' ' -f1)"
        [ -z "$expected" ] && error "Checksum entry not found for ${pkg_name}.tar.gz"
        actual="$(sha256_file "$tmp/harness.tar.gz")"
        [ "$actual" = "$expected" ] || error "Checksum mismatch — download may be corrupted"
    fi

    tar -xzf "$tmp/harness.tar.gz" -C "$tmp"
    for bin in $binaries; do
        mv "$tmp/$bin" "$dest/$bin"
        chmod +x "$dest/$bin"
        success "Installed $bin $version to $dest/$bin"
    done
    rm -rf "$tmp"
}

# ── shell config ───────────────────────────────────────────────────────────────

shell_config_block() {
    local shell_name
    shell_name="$(basename "${SHELL:-bash}")"
    printf '# <HarnessCLI>\n'
    if [ "$shell_name" = "fish" ]; then
        printf 'fish_add_path "$HOME/.local/bin"\n'
        printf 'harness completion fish | source\n'
    else
        printf 'export PATH="$HOME/.local/bin:$PATH"\n'
        printf 'source <(harness completion %s)\n' "$shell_name"
    fi
    printf '# </HarnessCLI>\n'
}

patch_shell_rc() {
    local rc="$1"
    touch "$rc"
    printf '\n%s\n\n' "$(shell_config_block)" >> "$rc"
}

already_patched() {
    local rc="$1"
    grep -q '<HarnessCLI>' "$rc" 2>/dev/null
}

# ── main ───────────────────────────────────────────────────────────────────────

main() {
    parse_args "$@"

    printf '\n  \033[1mHarness CLI installer\033[0m\n\n'

    local platform
    local version
    platform="$(detect_platform)"
    version="$(latest_version)"

    [ -z "$version" ] && error "Could not determine latest version"

    # create install dir if needed
    mkdir -p "$INSTALL_DIR"
    [ -d "$INSTALL_DIR" ] || error "$INSTALL_DIR is not a directory"

    # install binaries
    download_and_install "$version" "$platform" "$INSTALL_DIR"
    HARNESS_INSTALL_TYPE=script "$INSTALL_DIR/harness" --post-install >/dev/null 2>&1 || true

    local patched_rc=""

    # shell config — only if interactive and user didn't override install dir
    if is_interactive && [ -z "$USER_OVERRIDE" ]; then
        local rc
        local rc_name
        rc="$(detect_shell_rc)"
        rc_name="$(basename "$rc")"

        if already_patched "$rc"; then
            info "Shell config already set up in ~/$rc_name, skipping"
        else
            printf '\n'
            info "Would you like us to update ~/$rc_name?"
            info "  - Add ~/.local/bin to PATH"
            info "  - Add shell completions"
            printf '\n'

            if confirm "Update ~/$rc_name"; then
                patch_shell_rc "$rc"
                patched_rc="$rc_name"
                success "Updated ~/$rc_name"
            else
                printf '\n'
                info "To set up manually, add to ~/$rc_name:"
                printf '\n%s\n' "$(shell_config_block)"
            fi
        fi
    fi

    printf '\n'
    if command -v harness >/dev/null 2>&1; then
        success "Done! Run 'harness version' to verify."
    elif [ -n "$patched_rc" ]; then
        success "Done! Run 'source ~/$patched_rc' then 'harness version' to verify."
    else
        success "Done! Add $INSTALL_DIR to your PATH then run 'harness version' to verify."
    fi
}

main "$@"
