"""Build immutable Agent package manifests used by Control voice gating."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path


ROOT_METADATA_NAMES = {"manifest.json", "control-agent-allowlist.env"}
_SHA256_HEX = set("0123456789abcdef")


class PackageManifestError(ValueError):
    """The assembled package cannot be represented by a trusted manifest."""


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _iter_payload_files(root: Path):
    root = Path(root).absolute()
    if root.is_symlink():
        raise PackageManifestError(f"package root is a symlink: {root}")
    for dirpath, dirnames, filenames in os.walk(root):
        directory = Path(dirpath)
        for dirname in sorted(dirnames):
            path = directory / dirname
            if path.is_symlink():
                relative = path.relative_to(root).as_posix()
                raise PackageManifestError(f"symlink directory is not allowed: {relative}")
        for filename in sorted(filenames):
            path = directory / filename
            if path.is_symlink():
                relative = path.relative_to(root).as_posix()
                raise PackageManifestError(f"symlink file is not allowed: {relative}")
            relative = path.relative_to(root).as_posix()
            if relative in ROOT_METADATA_NAMES:
                continue
            if filename in ROOT_METADATA_NAMES:
                raise PackageManifestError(
                    f"nested package metadata is not allowed: {relative}")
            yield path, relative


def build_manifest(root: Path, *, architecture: str) -> dict:
    root = Path(root).absolute()
    files = []
    for path, relative in _iter_payload_files(root):
        files.append({
            "name": relative,
            "sha256": _sha256_file(path),
            "size": path.stat().st_size,
        })
    return {
        "version": 1,
        "architecture": architecture,
        "files": files,
    }


def _safe_manifest_name(value: object) -> str:
    if type(value) is not str:
        return ""
    raw = value.replace("\\", "/")
    if not raw or raw.startswith("/") or raw.startswith("../") or "/../" in raw:
        return ""
    normal = os.path.normpath(raw).replace("\\", "/")
    if normal in {"", "."} or normal.startswith("../") or normal.startswith("/"):
        return ""
    return normal


def _normalise_sha256(value: object) -> str:
    if type(value) is not str:
        return ""
    raw = value.strip().lower()
    if raw.startswith("sha256:"):
        raw = raw[7:]
    return raw if len(raw) == 64 and set(raw) <= _SHA256_HEX else ""


def verify_package_manifest(manifest_path: Path, *, expect_digest: str = "") -> str:
    manifest_path = Path(manifest_path).absolute()
    if manifest_path.is_symlink():
        raise PackageManifestError(f"manifest is a symlink: {manifest_path}")
    try:
        raw_manifest = manifest_path.read_bytes()
        manifest = json.loads(raw_manifest.decode("utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise PackageManifestError(f"cannot read package manifest: {exc}") from exc
    digest = hashlib.sha256(raw_manifest).hexdigest()
    expected_digest = _normalise_sha256(expect_digest) if expect_digest else ""
    if expect_digest and digest != expected_digest:
        raise PackageManifestError("package manifest digest does not match expected digest")
    if (not isinstance(manifest, dict) or
            type(manifest.get("version")) is not int or
            manifest.get("version") != 1):
        raise PackageManifestError("invalid manifest version")
    entries = manifest.get("files")
    if not isinstance(entries, list) or not entries:
        raise PackageManifestError("manifest contains no payload files")
    expected: dict[str, tuple[str, int]] = {}
    for entry in entries:
        if not isinstance(entry, dict):
            raise PackageManifestError("manifest file entry is not an object")
        if type(entry.get("name")) is not str or type(entry.get("sha256")) is not str:
            raise PackageManifestError("manifest file entry has invalid name or digest type")
        size = entry.get("size")
        if type(size) is not int:
            raise PackageManifestError("manifest file entry has invalid size type")
        name = _safe_manifest_name(entry.get("name"))
        file_digest = _normalise_sha256(entry.get("sha256"))
        if not name or not file_digest or size < 0 or name in expected:
            raise PackageManifestError(f"invalid or duplicate manifest path: {entry.get('name')}")
        expected[name] = (file_digest, size)

    root = manifest_path.parent
    observed: set[str] = set()
    for path, relative in _iter_payload_files(root):
        if relative not in expected:
            raise PackageManifestError(f"extra payload file is not in manifest: {relative}")
        observed.add(relative)
    missing = set(expected) - observed
    if missing:
        raise PackageManifestError(f"manifest payload is missing: {sorted(missing)[0]}")

    for relative, (expected_file_digest, expected_size) in expected.items():
        path = root.joinpath(*relative.split("/"))
        try:
            if path.stat().st_size != expected_size:
                raise PackageManifestError(f"payload size mismatch: {relative}")
            if _sha256_file(path) != expected_file_digest:
                raise PackageManifestError(f"payload digest mismatch: {relative}")
        except OSError as exc:
            raise PackageManifestError(f"cannot verify payload {relative}: {exc}") from exc
    return digest


def write_package_metadata(root: Path, *, architecture: str,
                           emit_allowlist: bool = True) -> str:
    root = Path(root).absolute()
    root.mkdir(parents=True, exist_ok=True)
    manifest_path = root / "manifest.json"
    allowlist_path = root / "control-agent-allowlist.env"
    manifest = build_manifest(root, architecture=architecture)
    payload = json.dumps(manifest, ensure_ascii=False, sort_keys=True,
                         separators=(",", ":")) + "\n"
    tmp_manifest = manifest_path.with_suffix(".json.tmp")
    tmp_manifest.write_text(payload, encoding="utf-8")
    os.replace(tmp_manifest, manifest_path)
    digest = verify_package_manifest(manifest_path)
    if emit_allowlist:
        tmp_allowlist = allowlist_path.with_suffix(".env.tmp")
        tmp_allowlist.write_text(
            f"MDD_ALLOWED_AGENT_PACKAGE_DIGESTS={digest}\n", encoding="utf-8")
        os.replace(tmp_allowlist, allowlist_path)
    elif allowlist_path.exists() or allowlist_path.is_symlink():
        allowlist_path.unlink()
    return digest


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("root", nargs="?", help="assembled package root")
    parser.add_argument("--architecture")
    parser.add_argument("--no-allowlist", action="store_true",
                        help="write manifest only; do not write Control allowlist metadata")
    parser.add_argument("--verify", metavar="MANIFEST",
                        help="verify an existing package-root manifest")
    parser.add_argument("--expect-digest", default="",
                        help="expected sha256 digest for --verify")
    args = parser.parse_args(argv)
    if args.verify:
        print(verify_package_manifest(Path(args.verify), expect_digest=args.expect_digest))
        return 0
    if not args.root or not args.architecture:
        parser.error("root and --architecture are required unless --verify is used")
    digest = write_package_metadata(
        Path(args.root), architecture=args.architecture,
        emit_allowlist=not args.no_allowlist)
    print(digest)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
