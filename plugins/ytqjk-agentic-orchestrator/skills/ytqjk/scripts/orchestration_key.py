"""Local HMAC key handling with strict platform permission checks."""

from __future__ import annotations

import csv
import io
import os
import re
import secrets
import stat
import subprocess
from pathlib import Path

from orchestration_models import LedgerUnavailable


_SYSTEM_SID = "S-1-5-18"
_ADMINS_SID = "S-1-5-32-544"
_SID = re.compile(r"S-1-\d+(?:-\d+)+", re.IGNORECASE)
_ACE = re.compile(r"^(?P<identity>.+?):(?P<rights>(?:\([A-Z,]+\))+)\s*$")
_SUMMARY = re.compile(
    r"^Successfully processed \d+ files; Failed processing \d+ files$",
    re.IGNORECASE,
)


class LocalHmacKey:
    """Create and verify a local HMAC key without exposing key material."""

    def __init__(self, path: Path):
        self.path = Path(path)

    def read(self, create: bool = False) -> bytes:
        """Return key only after strict least-privilege permission verification."""
        created = False
        if not self.path.exists():
            if not create:
                raise LedgerUnavailable("本机密钥不可用。")
            created = self._create()
        if os.name == "nt" and created:
            self._set_windows_acl()
        self._verify_permissions()
        key = self.path.read_bytes()
        if len(key) != 32:
            raise LedgerUnavailable("本机密钥无效。")
        return key

    def _create(self) -> bool:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        try:
            descriptor = os.open(self.path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        except FileExistsError:
            return False
        with os.fdopen(descriptor, "wb") as handle:
            handle.write(secrets.token_bytes(32))
        return True

    def _verify_permissions(self) -> None:
        if os.name != "nt":
            if stat.S_IMODE(self.path.stat().st_mode) & 0o077:
                raise LedgerUnavailable("本机密钥权限过宽。")
            return
        actual = self._windows_acl_entries()
        if not self._is_strict_windows_acl(actual):
            raise LedgerUnavailable("本机密钥 ACL 不满足最小权限。")

    def _set_windows_acl(self) -> None:
        allowed = [self._icacls_sid(self._current_sid()), self._icacls_sid(_SYSTEM_SID),
                   self._icacls_sid(_ADMINS_SID)]
        result = subprocess.run(
            ["icacls", str(self.path), "/inheritance:r", "/grant:r", *[f"{sid}:(F)" for sid in allowed]],
            capture_output=True,
            check=False,
        )
        if result.returncode != 0:
            raise LedgerUnavailable("本机密钥 ACL 不满足最小权限。")

    def _windows_acl_entries(self) -> dict[str, str]:
        result = subprocess.run(
            ["icacls", str(self.path)], capture_output=True, check=False,
            text=True, encoding="utf-8", errors="replace",
        )
        if result.returncode != 0:
            raise LedgerUnavailable("本机密钥 ACL 不可验证。")
        output = result.stdout + result.stderr
        if "(I)" in output.upper() or "(DENY)" in output.upper():
            raise LedgerUnavailable("本机密钥 ACL 不满足最小权限。")
        entries: dict[str, str] = {}
        for line in output.splitlines():
            candidate = line.strip()
            if not candidate or _SUMMARY.fullmatch(candidate):
                continue
            path_text = str(self.path)
            if candidate.casefold().startswith(path_text.casefold()):
                candidate = candidate[len(path_text):].strip()
            match = _ACE.fullmatch(candidate)
            if not match:
                raise LedgerUnavailable("本机密钥 ACL 不可验证。")
            identity = match.group("identity").upper()
            if identity in entries:
                raise LedgerUnavailable("本机密钥 ACL 不可验证。")
            entries[identity] = match.group("rights").upper()
        if not entries:
            raise LedgerUnavailable("本机密钥 ACL 不可验证。")
        return entries

    def _is_strict_windows_acl(self, entries: dict[str, str]) -> bool:
        user_sid = self._current_sid()
        user_name = self._current_name()
        groups = (
            {user_sid, user_name},
            {_SYSTEM_SID, "NT AUTHORITY\\SYSTEM", "SYSTEM"},
            {_ADMINS_SID, "BUILTIN\\ADMINISTRATORS", "ADMINISTRATORS"},
        )
        allowed = set().union(*groups)
        if set(entries) - allowed:
            return False
        if any("(F)" not in rights for rights in entries.values()):
            return False
        return all(any(identity in entries for identity in group) for group in groups)

    def _current_sid(self) -> str:
        result = subprocess.run(
            ["whoami", "/user", "/fo", "csv", "/nh"],
            capture_output=True, check=False, text=True, encoding="utf-8", errors="replace",
        )
        rows = list(csv.reader(io.StringIO(result.stdout)))
        if result.returncode or not rows or len(rows[0]) < 2:
            raise LedgerUnavailable("无法确定本机密钥所有者。")
        sid = rows[0][1].upper()
        if not _SID.fullmatch(sid):
            raise LedgerUnavailable("无法确定本机密钥所有者。")
        return sid

    def _current_name(self) -> str:
        result = subprocess.run(
            ["whoami"], capture_output=True, check=False, text=True,
            encoding="utf-8", errors="replace",
        )
        name = result.stdout.strip().upper()
        if result.returncode or not name:
            raise LedgerUnavailable("无法确定本机密钥所有者。")
        return name

    def _icacls_sid(self, sid: str) -> str:
        return f"*{sid}"
