from __future__ import annotations

import os
import hashlib
import io
import subprocess
import sys
import bz2
import lzma
import zipfile
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.intake_contracts import (  # noqa: E402
    CONTROLLED_SCANNER_ID,
    ScanResult,
    ScanState,
)
from scripts.intake_security import (  # noqa: E402
    IntakeSecurityError,
    inspect_input,
)


def test_safe_file_preserves_hash_and_source_metadata(tmp_path: Path) -> None:
    root = tmp_path / "sources"
    root.mkdir()
    source = root / "note.md"
    source.write_text("# Known\n\nEvidence", encoding="utf-8")

    inspected = inspect_input(root, source, purpose="deployment evidence")

    assert len(inspected) == 1
    assert inspected[0].relative_path == "note.md"
    assert inspected[0].purpose == "deployment evidence"
    expected_hash = hashlib.sha256(source.read_bytes()).hexdigest()
    assert inspected[0].sha256 == expected_hash


@pytest.mark.parametrize(
    "relative_path,content",
    [
        (".env", b"ordinary text"),
        (".env.local", b"ordinary text"),
        ("config/id_rsa", b"ordinary text"),
        ("config/client.pem", b"ordinary text"),
        ("note.txt", b"api_key = '0123456789abcdefghijklmnop'"),
        ("archive.zip", b"PK\x03\x04" + b"0" * 20),
        ("archive.zst", b"ordinary text"),
    ],
)
def test_sensitive_or_archive_input_fails_closed(
    tmp_path: Path, relative_path: str, content: bytes
) -> None:
    root = tmp_path / "sources"
    source = root / relative_path
    source.parent.mkdir(parents=True)
    source.write_bytes(content)

    with pytest.raises(IntakeSecurityError):
        inspect_input(root, source)


def test_outside_path_and_symlink_escape_are_rejected(tmp_path: Path) -> None:
    root = tmp_path / "sources"
    root.mkdir()
    outside = tmp_path / "outside.txt"
    outside.write_text("outside", encoding="utf-8")
    with pytest.raises(IntakeSecurityError, match="outside root"):
        inspect_input(root, outside)

    link = root / "escape"
    try:
        os.symlink(outside, link)
    except OSError:
        outside_directory = tmp_path / "outside"
        outside_directory.mkdir()
        result = subprocess.run(
            ["cmd", "/c", "mklink", "/J", str(link), str(outside_directory)],
            capture_output=True,
            check=False,
        )
        if result.returncode != 0:
            pytest.skip("link and junction creation unavailable")
    with pytest.raises(IntakeSecurityError, match="link or junction"):
        inspect_input(root, link)


def test_folder_walk_rejects_oversized_file(tmp_path: Path) -> None:
    root = tmp_path / "sources"
    root.mkdir()
    (root / "ok.txt").write_text("ok", encoding="utf-8")
    (root / "large.txt").write_bytes(b"x" * 17)

    with pytest.raises(IntakeSecurityError, match="too large"):
        inspect_input(root, root, max_file_bytes=16)


def test_unavailable_or_mismatched_scanner_fails_closed(tmp_path: Path) -> None:
    root = tmp_path / "sources"
    root.mkdir()
    source = root / "note.txt"
    source.write_text("safe", encoding="utf-8")

    class Scanner:
        def __init__(self, ready: bool) -> None:
            self._ready = ready

        def ready(self) -> bool:
            return self._ready

        def scan(self, content: bytes, phase: str) -> ScanResult:
            return ScanResult(
                ScanState.CLEAN, "0" * 64, len(content), CONTROLLED_SCANNER_ID
            )

    with pytest.raises(IntakeSecurityError, match="unavailable"):
        inspect_input(root, source, scanner=Scanner(False))
    with pytest.raises(IntakeSecurityError, match="does not match"):
        inspect_input(root, source, scanner=Scanner(True))


@pytest.mark.parametrize(
    "content",
    [
        bz2.compress(b"payload"),
        lzma.compress(b"payload", format=lzma.FORMAT_XZ),
        b"0" * 257 + b"ustar\x0000" + b"0" * 32,
        b"\x28\xb5\x2f\xfd" + b"payload",
    ],
)
def test_archive_magic_rejects_disguised_text(
    tmp_path: Path,
    content: bytes,
) -> None:
    root = tmp_path / "sources"
    root.mkdir()
    source = root / "archive.txt"
    source.write_bytes(content)
    with pytest.raises(IntakeSecurityError, match="archive"):
        inspect_input(root, source)


def test_same_size_file_replacement_during_scan_is_rejected(
    tmp_path: Path,
) -> None:
    root = tmp_path / "sources"
    root.mkdir()
    source = root / "note.txt"
    source.write_bytes(b"AAAA")

    class ReplacingScanner:
        def ready(self) -> bool:
            return True

        def scan(self, content: bytes, phase: str) -> ScanResult:
            source.write_bytes(b"BBBB")
            return ScanResult(
                ScanState.CLEAN,
                hashlib.sha256(content).hexdigest(),
                len(content),
                CONTROLLED_SCANNER_ID,
            )

    with pytest.raises(IntakeSecurityError, match="changed"):
        inspect_input(root, source, scanner=ReplacingScanner())


@pytest.mark.parametrize(
    "relative_path",
    [
        ".pgpass", "_netrc", ".my.cnf", ".cargo/config", ".m2/settings.xml",
        ".venv/auth.txt", "settings-security.xml", "client.ovpn",
        "state.tfstate.backup",
    ],
)
def test_representative_sensitive_paths_are_rejected(
    tmp_path: Path, relative_path: str
) -> None:
    root = tmp_path / "sources"
    source = root / relative_path
    source.parent.mkdir(parents=True)
    source.write_text("ordinary", encoding="utf-8")
    with pytest.raises(IntakeSecurityError, match="sensitive"):
        inspect_input(root, source)


@pytest.mark.parametrize(
    "relative_path", ["src/config.py", "config/app.yaml", "docs/settings.md"]
)
def test_normal_configuration_paths_are_allowed(
    tmp_path: Path, relative_path: str
) -> None:
    root = tmp_path / "sources"
    source = root / relative_path
    source.parent.mkdir(parents=True)
    source.write_text("ordinary", encoding="utf-8")
    assert inspect_input(root, source)[0].relative_path == relative_path


@pytest.mark.parametrize(
    "name", [".ssh", ".cargo", ".m2", ".venv", "client.pem"]
)
def test_sensitive_root_itself_is_rejected(tmp_path: Path, name: str) -> None:
    root = tmp_path / name
    root.mkdir()
    source = root / "ordinary.txt"
    source.write_text("ordinary", encoding="utf-8")
    with pytest.raises(IntakeSecurityError, match="root"):
        inspect_input(root, source)


@pytest.mark.parametrize("extension", [".docx", ".xlsx"])
def test_office_zip_container_reaches_not_configured_parser(
    tmp_path: Path, extension: str
) -> None:
    root = tmp_path / "sources"
    root.mkdir()
    source = root / f"office{extension}"
    source.write_bytes(_office_container(extension))
    inspected = inspect_input(root, source)[0]
    assert inspected.extension == extension
    from scripts.intake_parsers import default_registry

    with pytest.raises(ValueError, match="NOT_CONFIGURED"):
        default_registry().parse(inspected)


@pytest.mark.parametrize(
    "extension,members",
    [
        (".docx", {}),
        (".docx", {"[Content_Types].xml": b"x", "xl/workbook.xml": b"x"}),
        (".xlsx", {"[Content_Types].xml": b"x", "word/document.xml": b"x"}),
        (
            ".docx",
            {
                "[Content_Types].xml": b"x",
                "word/document.xml": b"x",
                "word/vbaProject.bin": b"macro",
            },
        ),
        (
            ".xlsx",
            {
                "[Content_Types].xml": b"x",
                "xl/workbook.xml": b"x",
                "../escape.xml": b"x",
            },
        ),
        (
            ".docx",
            {
                "[Content_Types].xml": b"x",
                "word/document.xml": b"x",
                "..\\escape.xml": b"x",
            },
        ),
    ],
)
def test_fake_or_unsafe_office_container_is_rejected(
    tmp_path: Path, extension: str, members: dict[str, bytes]
) -> None:
    root = tmp_path / "sources"
    root.mkdir()
    source = root / f"fake{extension}"
    source.write_bytes(_zip_members(members))
    with pytest.raises(IntakeSecurityError, match="Office"):
        inspect_input(root, source)


@pytest.mark.parametrize("content", [b"plain text", b"PK\x03\x04not-a-zip"])
def test_malformed_office_zip_is_rejected(
    tmp_path: Path, content: bytes
) -> None:
    root = tmp_path / "sources"
    root.mkdir()
    source = root / "fake.docx"
    source.write_bytes(content)
    with pytest.raises(IntakeSecurityError, match="Office"):
        inspect_input(root, source)


@pytest.mark.parametrize("extension", [".docx", ".xlsx"])
def test_office_container_with_corrupt_payload_is_rejected(
    tmp_path: Path, extension: str
) -> None:
    root = tmp_path / "sources"
    root.mkdir()
    source = root / f"corrupt{extension}"
    source.write_bytes(_corrupt_office_container(extension))
    with pytest.raises(IntakeSecurityError, match="invalid Office"):
        inspect_input(root, source)


def _office_container(extension: str) -> bytes:
    core = "word/document.xml" if extension == ".docx" else "xl/workbook.xml"
    return _zip_members({"[Content_Types].xml": b"types", core: b"document"})


def _zip_members(members: dict[str, bytes]) -> bytes:
    output = io.BytesIO()
    with zipfile.ZipFile(output, "w", zipfile.ZIP_DEFLATED) as archive:
        for name, content in members.items():
            archive.writestr(name, content)
    return output.getvalue()


def _corrupt_office_container(extension: str) -> bytes:
    core = "word/document.xml" if extension == ".docx" else "xl/workbook.xml"
    output = io.BytesIO()
    with zipfile.ZipFile(output, "w", zipfile.ZIP_STORED) as archive:
        archive.writestr("[Content_Types].xml", b"types")
        archive.writestr(core, b"document")
    content = bytearray(output.getvalue())
    content[content.index(b"document")] ^= 1
    return bytes(content)
