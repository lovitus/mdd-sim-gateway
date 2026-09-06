#!/usr/bin/env bash
# ==============================================================================
# MDD VoWiFi Gateway - 离线一键导入与安装脚本 (Offline 1-Click Installer)
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GO_INSTALLER="${SCRIPT_DIR}/go-runtime/release/install-release.sh"

fail() {
    echo "[!] $*" >&2
    exit 2
}

# The offline path accepts only an immutable Go release directory or archive.
    if [[ "${1:-}" != "install" || $# -ne 2 ]]; then
        exec "$GO_INSTALLER" "$@"
    fi

    [[ "$(id -u)" -eq 0 ]] || fail "离线安装需要 root 权限。"
    SOURCE=$2
    [[ "$SOURCE" = /* ]] || fail "Go release 目录或归档必须使用绝对路径。"
    SOURCE_CANON=$(realpath -e -- "$SOURCE") || fail "Go release 不存在。"
    [[ "$SOURCE_CANON" == "$SOURCE" ]] || fail "Go release 路径必须规范化，不能包含符号链接、点段或尾随斜杠。"

    if [[ -d "$SOURCE_CANON" ]]; then
        if [[ -f "$SOURCE_CANON/manifest.json" ]]; then
            exec "$GO_INSTALLER" install "$SOURCE_CANON"
        fi
        [[ -f "$SOURCE_CANON/install-release.sh" && -x "$SOURCE_CANON/install-release.sh" &&
            ! -L "$SOURCE_CANON/install-release.sh" ]] ||
            fail "外层 Go artifact 缺少 install-release.sh。"
        mapfile -t OUTER_ENTRIES < <(
            find "$SOURCE_CANON" -mindepth 1 -maxdepth 1 -print
        )
        [[ ${#OUTER_ENTRIES[@]} -eq 2 ]] ||
            fail "外层 Go artifact 只能包含 install-release.sh 和一个 release 目录。"
        mapfile -t RELEASE_DIRECTORIES < <(
            find "$SOURCE_CANON" -mindepth 1 -maxdepth 1 -type d -name 'mdd-*' -print
        )
        [[ ${#RELEASE_DIRECTORIES[@]} -eq 1 && -f "${RELEASE_DIRECTORIES[0]}/manifest.json" ]] ||
            fail "外层 Go artifact 必须只包含一个严格 release 目录。"
        exec "$SOURCE_CANON/install-release.sh" install "${RELEASE_DIRECTORIES[0]}"
    fi

    [[ -f "$SOURCE_CANON" && ! -L "$SOURCE_CANON" ]] || fail "Go release artifact 必须是普通文件或目录。"
    command -v tar >/dev/null 2>&1 || fail "缺少 tar。"
    EXTRACT_ROOT=$(mktemp -d /var/tmp/mdd-go-offline.XXXXXX)
    trap 'rm -rf -- "$EXTRACT_ROOT"' EXIT HUP INT TERM

    # GNU/bsdtar already reject unsafe traversal during extraction; keep an
    # explicit format boundary too, and reject links/devices before writing.
    declare -A TOP_LEVELS=()
    while IFS= read -r entry; do
        entry=${entry#./}
        [[ -n "$entry" && "$entry" != /* ]] || fail "artifact 包含绝对或空路径。"
        case "/$entry/" in
            */../*|*/./*) fail "artifact 包含路径穿越点段。" ;;
        esac
        top=${entry%%/*}
        [[ "$top" == "install-release.sh" || "$top" == mdd-* ]] ||
            fail "artifact 包含未知顶层路径：$top"
        TOP_LEVELS["$top"]=1
    done < <(tar -tf "$SOURCE_CANON")
    [[ ${#TOP_LEVELS[@]} -eq 2 && -n "${TOP_LEVELS[install-release.sh]+present}" ]] ||
        fail "artifact 只能包含 install-release.sh 和一个 release 目录。"
    RELEASE_TOPS=()
    for top in "${!TOP_LEVELS[@]}"; do
        [[ "$top" == mdd-* ]] && RELEASE_TOPS+=("$top")
    done
    [[ ${#RELEASE_TOPS[@]} -eq 1 ]] || fail "artifact 包含多个 release 顶层目录。"
    EXPECTED_RELEASE_TOP=${RELEASE_TOPS[0]}
    while IFS= read -r mode _; do
        case "${mode:0:1}" in
            -|d) ;;
            *) fail "artifact 包含符号链接、硬链接或设备节点。" ;;
        esac
    done < <(tar -tvf "$SOURCE_CANON")

    umask 022
    tar --extract --file "$SOURCE_CANON" --directory "$EXTRACT_ROOT" \
        --no-same-owner --no-same-permissions
    [[ -f "$EXTRACT_ROOT/install-release.sh" && -x "$EXTRACT_ROOT/install-release.sh" &&
        ! -L "$EXTRACT_ROOT/install-release.sh" ]] ||
        fail "artifact 缺少可执行 install-release.sh。"
    mapfile -t RELEASE_DIRECTORIES < <(
        find "$EXTRACT_ROOT" -mindepth 1 -maxdepth 1 -type d -name 'mdd-*' -print
    )
    [[ ${#RELEASE_DIRECTORIES[@]} -eq 1 &&
        "$(basename "${RELEASE_DIRECTORIES[0]}")" == "$EXPECTED_RELEASE_TOP" &&
        -f "${RELEASE_DIRECTORIES[0]}/manifest.json" ]] ||
        fail "artifact 必须只包含一个严格 release 目录。"
    "$EXTRACT_ROOT/install-release.sh" install "${RELEASE_DIRECTORIES[0]}"
    exit 0
