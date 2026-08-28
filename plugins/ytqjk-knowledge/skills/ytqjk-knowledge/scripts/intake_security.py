import hashlib
import io
import mimetypes
import os
import re
import stat
import zipfile
from pathlib import Path, PurePosixPath

from .intake_contracts import CONTROLLED_SCANNER_ID, InspectedSource
from .intake_contracts import ScanResult, ScannerPort, ScanState
from .intake_paths import is_link_or_junction


DEFAULT_MAX_FILE_BYTES = 10 * 1024 * 1024
_OFFICE_LIMITS = (256, 12, 64 * 1024 * 1024)
_ARCHIVE_SUFFIXES = frozenset(
    ".7z .bz2 .gz .rar .tar .tgz .xz .zip .zst".split())
_ARCHIVE_MAGIC = (
    b"PK\x03\x04", b"PK\x05\x06", b"7z\xbc\xaf\x27\x1c", b"Rar!",
    b"\x1f\x8b", b"BZh", b"\xfd7zXZ\x00", b"\x28\xb5\x2f\xfd",
)
_SENSITIVE_NAMES = frozenset(
    (
        ".authinfo .env .git-credentials .my.cnf .netrc .npmrc .pgpass "
        ".pypirc _netrc auth.json credentials credentials.json "
        "credentials.toml "
        "gradle.properties id_dsa id_ecdsa id_ed25519 id_rsa kubeconfig "
        "nuget.config secret.json secrets secrets.json service-account.json "
        "service_account.json settings-security.xml token.json tokens.json"
    ).split()
)
_SENSITIVE_DIRS = frozenset(
    ".aws .azure .cargo .docker .gnupg .kube .m2 .ssh .terraform .venv".split())
_SENSITIVE_ENDINGS = tuple(
    (
        ".age .asc .gpg .jks .kdbx .key .keystore .p12 .pem .pfx .ovpn "
        ".tfstate .tfstate.backup .tfvars .tfvars.json"
    ).split())
_SECRET_PATTERNS = (
    re.compile(rb"-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----"),
    re.compile(
        rb"(?i)(?:api[_-]?key|access[_-]?token|client[_-]?secret|password)"
        rb"\s*[:=]\s*['\"]?[A-Za-z0-9_./+=-]{16,}"
    ),
    re.compile(rb"AKIA[0-9A-Z]{16}"),
    re.compile(rb"gh[pousr]_[A-Za-z0-9]{30,}"),
)


class IntakeSecurityError(ValueError):
    """Raised when intake input fails security validation."""


class LocalScanner:
    """Scan local bytes with deterministic secret patterns."""

    def ready(self) -> bool:
        """Return whether scanner can accept work."""
        return True

    def scan(self, content: bytes, phase: str) -> ScanResult:
        """Scan content and return digest-bound result."""
        del phase
        blocked = any(pattern.search(content) for pattern in _SECRET_PATTERNS)
        state = ScanState.BLOCKED if blocked else ScanState.CLEAN
        return ScanResult(
            state,
            hashlib.sha256(content).hexdigest(),
            len(content),
            CONTROLLED_SCANNER_ID,
        )


def inspect_input(
    root: Path,
    source: Path,
    *,
    purpose: str | None = None,
    max_file_bytes: int = DEFAULT_MAX_FILE_BYTES,
    scanner: ScannerPort | None = None,
) -> tuple[InspectedSource, ...]:
    """Validate and inspect file or directory input within root."""
    if max_file_bytes <= 0:
        raise IntakeSecurityError("max file size must be positive")
    active_scanner = scanner or LocalScanner()
    try:
        scanner_ready = active_scanner.ready()
    except Exception as error:
        raise IntakeSecurityError("scanner unavailable") from error
    if not scanner_ready:
        raise IntakeSecurityError("scanner unavailable")
    root_path = _resolve_directory(root)
    source_path = _contained_source(root_path, source)
    if source_path.is_dir():
        files = sorted(
            (path for path in source_path.rglob("*") if path.is_file()),
            key=lambda path: path.relative_to(root_path).as_posix(),
        )
        for path in source_path.rglob("*"):
            _reject_link_components(root_path, path)
            _reject_sensitive_path(root_path, path)
        return tuple(
            _inspect_file(
                root_path,
                path,
                purpose,
                max_file_bytes,
                active_scanner,
            )
            for path in files
        )
    return (
        _inspect_file(
            root_path,
            source_path,
            purpose,
            max_file_bytes,
            active_scanner,
        ),
    )


def _resolve_directory(root: Path) -> Path:
    try:
        resolved = root.resolve(strict=True)
    except (OSError, RuntimeError) as error:
        raise IntakeSecurityError("input root is unavailable") from error
    if not resolved.is_dir():
        raise IntakeSecurityError("input root must be a directory")
    _reject_sensitive_root(resolved)
    return resolved


def _reject_sensitive_root(root: Path) -> None:
    names = {part.casefold() for part in root.parts}
    name = root.name.casefold()
    if (
        names & _SENSITIVE_DIRS
        or name in _SENSITIVE_NAMES
        or name.startswith(".env")
        or name.endswith(_SENSITIVE_ENDINGS)
    ):
        raise IntakeSecurityError("sensitive input root is forbidden")


def _contained_source(root: Path, source: Path) -> Path:
    candidate = source if source.is_absolute() else root / source
    if is_link_or_junction(candidate):
        raise IntakeSecurityError("link or junction input is forbidden")
    try:
        absolute = candidate.absolute()
        absolute.relative_to(root)
    except ValueError as error:
        raise IntakeSecurityError("input is outside root") from error
    try:
        absolute.stat(follow_symlinks=False)
        _reject_link_components(root, absolute)
        absolute.resolve(strict=True).relative_to(root)
    except ValueError as error:
        raise IntakeSecurityError("input is outside root") from error
    except (OSError, RuntimeError) as error:
        raise IntakeSecurityError("input is unavailable") from error
    return absolute


def _reject_link_components(root: Path, path: Path) -> None:
    try:
        relative = path.absolute().relative_to(root)
    except ValueError as error:
        raise IntakeSecurityError("input is outside root") from error
    current = root
    for part in relative.parts:
        current /= part
        if is_link_or_junction(current):
            raise IntakeSecurityError("link or junction input is forbidden")


def _reject_sensitive_path(root: Path, path: Path) -> None:
    parts = path.relative_to(root).parts
    names = tuple(part.casefold() for part in parts)
    if any(
        name in _SENSITIVE_NAMES
        or name in _SENSITIVE_DIRS
        or name.startswith(".env.")
        or name.endswith(_SENSITIVE_ENDINGS)
        for name in names
    ):
        raise IntakeSecurityError("sensitive file or directory is forbidden")


def _inspect_file(
    root: Path,
    path: Path,
    purpose: str | None,
    max_file_bytes: int,
    scanner: ScannerPort,
) -> InspectedSource:
    _reject_link_components(root, path)
    _reject_sensitive_path(root, path)
    suffix = path.suffix.casefold()
    if suffix in _ARCHIVE_SUFFIXES:
        raise IntakeSecurityError("archive input is forbidden")
    try:
        with path.open("rb") as stream:
            before = os.fstat(stream.fileno())
            if not stat.S_ISREG(before.st_mode):
                raise IntakeSecurityError(
                    "device or non-regular input is forbidden"
                )
            if before.st_size > max_file_bytes:
                raise IntakeSecurityError("input file is too large")
            content = stream.read(max_file_bytes + 1)
            if len(content) != before.st_size:
                raise IntakeSecurityError("input changed while reading")
            _reject_archive_magic(content, suffix)
            scan = _scan(scanner, content)
            stream.seek(0)
            stable_content = stream.read(max_file_bytes + 1)
            after = os.fstat(stream.fileno())
    except OSError as error:
        raise IntakeSecurityError("input cannot be read") from error
    try:
        final = path.stat(follow_symlinks=False)
    except OSError as error:
        raise IntakeSecurityError("input changed while reading") from error
    if (
        stable_content != content
        or not _same_file(before, after, include_ctime=True)
        or not _same_file(after, final, include_ctime=False)
    ):
        raise IntakeSecurityError("input changed while reading")
    digest = hashlib.sha256(content).hexdigest()
    if scan.state is not ScanState.CLEAN:
        raise IntakeSecurityError(
            f"scanner rejected source: {scan.state.value}"
        )
    if scan.sha256 != digest or scan.size_bytes != len(content):
        raise IntakeSecurityError("scanner output does not match source")
    return InspectedSource(
        root=root,
        path=path,
        relative_path=path.relative_to(root).as_posix(),
        extension=suffix,
        media_type=mimetypes.guess_type(path.name, strict=True)[0],
        content=content,
        sha256=digest,
        size_bytes=len(content),
        modified_ns=final.st_mtime_ns,
        changed_ns=final.st_ctime_ns,
        device=final.st_dev,
        inode=final.st_ino,
        scan=scan,
        purpose=purpose.strip() if purpose and purpose.strip() else None,
    )


def _reject_archive_magic(content: bytes, suffix: str) -> None:
    if suffix in {".docx", ".xlsx"}:
        if not content.startswith(_ARCHIVE_MAGIC[:2]):
            raise IntakeSecurityError("invalid Office container")
        _validate_office_container(content, suffix)
        return
    if any(content.startswith(magic) for magic in _ARCHIVE_MAGIC):
        raise IntakeSecurityError("archive declaration is forbidden")
    if len(content) >= 262 and content[257:262] == b"ustar":
        raise IntakeSecurityError("archive declaration is forbidden")


def _validate_office_container(content: bytes, suffix: str) -> None:
    cores = {".docx": "word/document.xml", ".xlsx": "xl/workbook.xml"}
    required = cores[suffix]
    try:
        with zipfile.ZipFile(io.BytesIO(content), "r") as archive:
            members = archive.infolist()
            _validate_office_members(members)
            names = {member.filename for member in members}
            tuple(archive.read(member) for member in members)
    except Exception as error:
        raise IntakeSecurityError("invalid Office container") from error
    if "[Content_Types].xml" not in names or required not in names:
        raise IntakeSecurityError("Office container structure is invalid")


def _validate_office_members(members: list[zipfile.ZipInfo]) -> None:
    member_limit, depth_limit, expanded_limit = _OFFICE_LIMITS
    names = {member.filename for member in members}
    if not members or len(members) > member_limit or len(names) != len(members):
        raise IntakeSecurityError("Office container member limit exceeded")
    expanded = 0
    for member in members:
        path = PurePosixPath(member.filename)
        parts = path.parts
        if (
            not parts
            or path.is_absolute()
            or ".." in parts
            or "\\" in member.filename
            or any(":" in part for part in parts)
            or len(parts) > depth_limit
            or member.flag_bits & 0x1
            or path.name.casefold() == "vbaproject.bin"
        ):
            raise IntakeSecurityError("unsafe Office container member")
        expanded += member.file_size
        if expanded > expanded_limit:
            raise IntakeSecurityError(
                "Office container expanded limit exceeded"
            )


def _scan(scanner: ScannerPort, content: bytes) -> ScanResult:
    try:
        result = scanner.scan(content, "source")
    except Exception as error:
        raise IntakeSecurityError("scanner failed") from error
    if not isinstance(result, ScanResult):
        raise IntakeSecurityError("scanner output is invalid")
    if result.scanner != CONTROLLED_SCANNER_ID:
        raise IntakeSecurityError("scanner identity is invalid")
    return result


def _same_file(*values: os.stat_result, include_ctime: bool) -> bool:
    fields = ["st_dev", "st_ino", "st_size", "st_mtime_ns"]
    if include_ctime:
        fields.append("st_ctime_ns")
    baseline = tuple(getattr(values[0], field, None) for field in fields)
    snapshots = (
        tuple(getattr(value, field, None) for field in fields)
        for value in values[1:])
    return all(snapshot == baseline for snapshot in snapshots)
