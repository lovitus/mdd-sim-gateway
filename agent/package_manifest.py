"""Build immutable Agent package manifests used by Control voice gating."""

from __future__ import annotations

import argparse
import contextlib
import hashlib
import json
import os
import re
import shutil
import tempfile
from pathlib import Path


ROOT_METADATA_NAMES = {"manifest.json", "control-agent-allowlist.env"}
_SHA256_HEX = set("0123456789abcdef")
_MANIFEST_KEYS = {"version", "architecture", "files"}
_FILE_ENTRY_KEYS = {"name", "size", "sha256"}
_ARCHITECTURE_RE = re.compile(r"^[a-z0-9][a-z0-9-]{1,63}$")
_RELEASE_ARCHITECTURES = {
    "macos-arm64": Path("agent/dist/mdd-agent-macos-arm64"),
    "windows-amd64": Path("agent/dist/mdd-agent-windows-amd64"),
}


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
    if type(architecture) is not str or not _ARCHITECTURE_RE.fullmatch(architecture):
        raise PackageManifestError(f"invalid package architecture: {architecture!r}")
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


def verify_package_manifest(manifest_path: Path, *, expect_digest: str = "",
                            expect_architecture: str = "") -> str:
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
    if (not isinstance(manifest, dict) or set(manifest) != _MANIFEST_KEYS or
            type(manifest.get("version")) is not int or
            manifest.get("version") != 1):
        raise PackageManifestError("invalid manifest schema or version")
    architecture = manifest.get("architecture")
    if (type(architecture) is not str or
            not _ARCHITECTURE_RE.fullmatch(architecture)):
        raise PackageManifestError("invalid manifest architecture")
    if expect_architecture and architecture != expect_architecture:
        raise PackageManifestError(
            f"package architecture {architecture!r} does not match "
            f"{expect_architecture!r}")
    entries = manifest.get("files")
    if not isinstance(entries, list) or not entries:
        raise PackageManifestError("manifest contains no payload files")
    expected: dict[str, tuple[str, int]] = {}
    for entry in entries:
        if not isinstance(entry, dict) or set(entry) != _FILE_ENTRY_KEYS:
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


def _assert_no_symlink_components(path: Path) -> None:
    """Reject symlinks in every existing path component, including broken links."""
    path = Path(path).absolute()
    current = Path(path.anchor)
    for part in path.parts[1:]:
        current /= part
        if os.path.lexists(current) and current.is_symlink():
            raise PackageManifestError(f"release path component is a symlink: {current}")


def _ensure_real_directory(path: Path, *, mode: int = 0o700) -> Path:
    path = Path(path).absolute()
    _assert_no_symlink_components(path)
    existed = os.path.lexists(path)
    path.mkdir(parents=True, exist_ok=True, mode=mode)
    _assert_no_symlink_components(path)
    if not path.is_dir():
        raise PackageManifestError(f"release path is not a directory: {path}")
    if not existed:
        _fsync_directory(path)
        _fsync_directory(path.parent)
    return path


def _fsync_directory(path: Path) -> None:
    descriptor = os.open(path, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def _fsync_tree(root: Path) -> None:
    for dirpath, dirnames, filenames in os.walk(root, topdown=False):
        directory = Path(dirpath)
        for filename in filenames:
            path = directory / filename
            with path.open("rb") as handle:
                os.fsync(handle.fileno())
        for dirname in dirnames:
            _fsync_directory(directory / dirname)
    _fsync_directory(root)


def _parse_digest_list(raw: str) -> set[str]:
    result: set[str] = set()
    for value in re.split(r"[,;\s]+", str(raw or "")):
        if not value:
            continue
        digest = _normalise_sha256(value)
        if not digest:
            raise PackageManifestError(f"invalid Agent package digest: {value}")
        result.add(digest)
    return result


def _verify_release_anchor(root: Path, digest: str) -> None:
    anchor = root / "control-agent-allowlist.env"
    if anchor.is_symlink() or not anchor.is_file():
        raise PackageManifestError(
            f"release package is missing its digest trust anchor: {anchor}")
    try:
        value = anchor.read_bytes()
    except OSError as exc:
        raise PackageManifestError(f"cannot read release trust anchor: {exc}") from exc
    expected = f"MDD_ALLOWED_AGENT_PACKAGE_DIGESTS={digest}\n".encode("ascii")
    if value != expected:
        raise PackageManifestError(
            "release trust anchor does not match the verified manifest digest")


@contextlib.contextmanager
def _release_lock(architecture_root: Path):
    try:
        import fcntl
    except ImportError as exc:  # pragma: no cover - install.sh uses this only on Linux/macOS.
        raise PackageManifestError("release publication needs POSIX file locking") from exc
    lock_path = architecture_root / ".publish.lock"
    with lock_path.open("a+b") as handle:
        fcntl.flock(handle.fileno(), fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(handle.fileno(), fcntl.LOCK_UN)


def _publish_release_package(source: Path, architecture_root: Path, *,
                             architecture: str, digest: str) -> Path:
    destination = architecture_root / digest
    with _release_lock(architecture_root):
        if os.path.lexists(destination):
            if destination.is_symlink() or not destination.is_dir():
                raise PackageManifestError(
                    f"release digest path is not a real directory: {destination}")
            verify_package_manifest(
                destination / "manifest.json", expect_digest=digest,
                expect_architecture=architecture)
            _verify_release_anchor(destination, digest)
            # A prior invocation may have completed rename but reported a parent fsync
            # failure. Reuse must retry that durability barrier before claiming success.
            _fsync_directory(architecture_root)
            return destination

        staging = Path(tempfile.mkdtemp(
            prefix=".staging-", dir=str(architecture_root)))
        try:
            # copytree requires a non-existent target; keep the private mkdtemp name but
            # replace the empty directory before copying the complete verified package.
            staging.rmdir()
            shutil.copytree(source, staging, symlinks=False)
            verify_package_manifest(
                staging / "manifest.json", expect_digest=digest,
                expect_architecture=architecture)
            _verify_release_anchor(staging, digest)
            _fsync_tree(staging)
            os.rename(staging, destination)
            _fsync_directory(architecture_root)
            return destination
        finally:
            if os.path.lexists(staging):
                shutil.rmtree(staging, ignore_errors=True)


def collect_release_allowlist(repo_root: Path, data_root: Path, *,
                              raw_digests: str = "") -> list[str]:
    """Verify/persist repository Agent packages and return the trusted digest union."""
    repo_root = Path(repo_root).absolute()
    data_root = _ensure_real_directory(Path(data_root), mode=0o700)
    release_root = _ensure_real_directory(data_root / "agent-releases", mode=0o700)
    digests = _parse_digest_list(raw_digests)

    for architecture, relative in _RELEASE_ARCHITECTURES.items():
        architecture_root = _ensure_real_directory(
            release_root / architecture, mode=0o700)
        source = repo_root / relative
        if os.path.lexists(source):
            if source.is_symlink() or not source.is_dir():
                raise PackageManifestError(
                    f"Agent package artifact is not a real directory: {source}")
            digest = verify_package_manifest(
                source / "manifest.json", expect_architecture=architecture)
            anchor = source / "control-agent-allowlist.env"
            unsigned_marker = source / "UNSIGNED_DEVELOPMENT_ARTIFACT"
            if architecture == "macos-arm64" and not os.path.lexists(anchor) and \
                    unsigned_marker.is_file() and \
                    not unsigned_marker.is_symlink():
                # A fully verified, explicitly unsigned macOS development package is
                # intentionally ineligible for new Control trust. Existing persistent
                # signed releases for this architecture must still be scanned below.
                pass
            else:
                _verify_release_anchor(source, digest)
                _publish_release_package(
                    source, architecture_root, architecture=architecture, digest=digest)
                digests.add(digest)

        stored_release_seen = False
        for candidate in sorted(architecture_root.iterdir()):
            if not re.fullmatch(r"[0-9a-f]{64}", candidate.name):
                continue
            verify_package_manifest(
                candidate / "manifest.json", expect_digest=candidate.name,
                expect_architecture=architecture)
            _verify_release_anchor(candidate, candidate.name)
            digests.add(candidate.name)
            stored_release_seen = True
        if stored_release_seen:
            _fsync_directory(architecture_root)
    return sorted(digests)


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
        tmp_allowlist.write_bytes(
            f"MDD_ALLOWED_AGENT_PACKAGE_DIGESTS={digest}\n".encode("ascii"))
        os.replace(tmp_allowlist, allowlist_path)
        _verify_release_anchor(root, digest)
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
    parser.add_argument("--expect-architecture", default="",
                        help="expected architecture for --verify")
    parser.add_argument("--collect-release-allowlist", action="store_true",
                        help="verify/persist repository artifacts and print digest union")
    parser.add_argument("--repo-root", help="repository root for release collection")
    parser.add_argument("--data-root", help="persistent data root for release collection")
    parser.add_argument("--raw-digests", default="",
                        help="operator-provided digest union for release collection")
    args = parser.parse_args(argv)
    if args.collect_release_allowlist:
        if not args.repo_root or not args.data_root:
            parser.error("--repo-root and --data-root are required for release collection")
        print(",".join(collect_release_allowlist(
            Path(args.repo_root), Path(args.data_root), raw_digests=args.raw_digests)))
        return 0
    if args.verify:
        print(verify_package_manifest(
            Path(args.verify), expect_digest=args.expect_digest,
            expect_architecture=args.expect_architecture))
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
