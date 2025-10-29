#!/bin/sh
# GitHub Brain installer script
# Usage: curl -fsSL https://raw.githubusercontent.com/wham/github-brain/main/install.sh | sh

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Repository information
REPO="wham/github-brain"
BINARY_NAME="github-brain"

# Detect OS
detect_os() {
    case "$(uname -s)" in
        Darwin*)
            echo "darwin"
            ;;
        Linux*)
            echo "linux"
            ;;
        MINGW*|MSYS*|CYGWIN*)
            echo "windows"
            ;;
        *)
            printf "${RED}Error: Unsupported operating system$(uname -s)${NC}\n" >&2
            exit 1
            ;;
    esac
}

# Detect architecture
detect_arch() {
    case "$(uname -m)" in
        x86_64|amd64)
            echo "amd64"
            ;;
        arm64|aarch64)
            echo "arm64"
            ;;
        *)
            printf "${RED}Error: Unsupported architecture $(uname -m)${NC}\n" >&2
            exit 1
            ;;
    esac
}

# Determine install directory
get_install_dir() {
    if [ -w "/usr/local/bin" ]; then
        echo "/usr/local/bin"
    elif [ -d "$HOME/.local/bin" ]; then
        echo "$HOME/.local/bin"
    else
        # Create ~/.local/bin if it doesn't exist
        mkdir -p "$HOME/.local/bin"
        echo "$HOME/.local/bin"
    fi
}

# Main installation
main() {
    printf "${GREEN}Installing GitHub Brain...${NC}\n"
    
    # Detect platform
    OS=$(detect_os)
    ARCH=$(detect_arch)
    
    # Determine archive format and binary name
    if [ "$OS" = "windows" ]; then
        ARCHIVE_EXT="zip"
        BINARY_NAME="${BINARY_NAME}.exe"
    else
        ARCHIVE_EXT="tar.gz"
    fi
    
    ARCHIVE_NAME="${BINARY_NAME%-*}-${OS}-${ARCH}.${ARCHIVE_EXT}"
    DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/github-brain-${OS}-${ARCH}.${ARCHIVE_EXT}"
    
    printf "Platform: ${YELLOW}${OS}-${ARCH}${NC}\n"
    printf "Downloading from: ${YELLOW}${DOWNLOAD_URL}${NC}\n"
    
    # Create temporary directory
    TMP_DIR=$(mktemp -d)
    trap 'rm -rf "$TMP_DIR"' EXIT
    
    # Download archive
    printf "Downloading...\n"
    if ! curl -fsSL "$DOWNLOAD_URL" -o "$TMP_DIR/archive.${ARCHIVE_EXT}"; then
        printf "${RED}Error: Failed to download ${BINARY_NAME}${NC}\n" >&2
        exit 1
    fi
    
    # Extract archive
    printf "Extracting...\n"
    cd "$TMP_DIR"
    if [ "$ARCHIVE_EXT" = "zip" ]; then
        if ! unzip -q "archive.${ARCHIVE_EXT}"; then
            printf "${RED}Error: Failed to extract archive${NC}\n" >&2
            exit 1
        fi
    else
        if ! tar -xzf "archive.${ARCHIVE_EXT}"; then
            printf "${RED}Error: Failed to extract archive${NC}\n" >&2
            exit 1
        fi
    fi
    
    # Determine install directory
    INSTALL_DIR=$(get_install_dir)
    printf "Installing to: ${YELLOW}${INSTALL_DIR}${NC}\n"
    
    # Move binary to install directory
    if [ -w "$INSTALL_DIR" ]; then
        mv "${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"
        chmod +x "${INSTALL_DIR}/${BINARY_NAME}"
    else
        # Need sudo for system directory
        printf "Installing to system directory requires sudo...\n"
        sudo mv "${BINARY_NAME}" "${INSTALL_DIR}/${BINARY_NAME}"
        sudo chmod +x "${INSTALL_DIR}/${BINARY_NAME}"
    fi
    
    # Verify installation
    if [ -x "${INSTALL_DIR}/${BINARY_NAME}" ]; then
        VERSION=$("${INSTALL_DIR}/${BINARY_NAME}" --version 2>/dev/null || echo "unknown")
        printf "${GREEN}✓ GitHub Brain installed successfully!${NC}\n"
        printf "Version: ${YELLOW}${VERSION}${NC}\n"
        printf "Location: ${YELLOW}${INSTALL_DIR}/${BINARY_NAME}${NC}\n"
        
        # Check if install dir is in PATH
        case ":$PATH:" in
            *":${INSTALL_DIR}:"*)
                printf "\n${GREEN}Ready to use! Run:${NC} ${BINARY_NAME%-*} --help\n"
                ;;
            *)
                printf "\n${YELLOW}Note: ${INSTALL_DIR} is not in your PATH${NC}\n"
                printf "Add this to your shell profile (~/.bashrc, ~/.zshrc, etc.):\n"
                printf "  export PATH=\"\$PATH:${INSTALL_DIR}\"\n"
                ;;
        esac
    else
        printf "${RED}Error: Installation verification failed${NC}\n" >&2
        exit 1
    fi
}

main
