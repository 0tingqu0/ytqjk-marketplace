from __future__ import annotations

import hashlib
import os
import re
from pathlib import PurePosixPath
from urllib.parse import urlsplit, urlunsplit


SKIP_PARTS = {
    ".git",
    ".venv",
    "node_modules",
    "vendor",
    "dist",
    "build",
    "coverage",
    "__pycache__",
}
SECRET_DIRS = {
    ".aws",
    ".azure",
    ".cargo",
    ".docker",
    ".gnupg",
    ".kube",
    ".m2",
    ".ssh",
    ".terraform",
}
SENSITIVE_NAMES = {
    ".authinfo",
    ".git-credentials",
    ".my.cnf",
    ".netrc",
    ".npmrc",
    ".pgpass",
    ".pypirc",
    ".yarnrc",
    ".yarnrc.yml",
    "_netrc",
    "auth.json",
    "credentials",
    "credentials.json",
    "credentials.toml",
    "gradle.properties",
    "id_ed25519",
    "id_rsa",
    "kubeconfig",
    "nuget.config",
    "secret.json",
    "secrets.json",
    "service-account.json",
    "service_account.json",
    "settings-security.xml",
    "token.json",
    "tokens.json",
}
SENSITIVE_ENDINGS = (
    ".age",
    ".asc",
    ".gpg",
    ".jks",
    ".kdbx",
    ".key",
    ".keystore",
    ".p12",
    ".pem",
    ".pfx",
    ".ovpn",
    ".tfstate",
    ".tfstate.backup",
    ".tfvars",
    ".tfvars.json",
)
SENSITIVE_CONFIG = re.compile(
    r"(?:auth|credential|credentials|secret|secrets|token|tokens)"
    r"\.(?:cfg|conf|ini|json|properties|toml|ya?ml)"
)
SECRET_PATTERNS = (
    re.compile(
        r"-----BEGIN (?:(?:OPENSSH|RSA|EC|DSA|PGP) )?PRIVATE KEY(?: BLOCK)?-----"
    ),
    re.compile(r"\bgh[opusr]_[A-Za-z0-9]{20,}\b"),
    re.compile(r"\bgithub_pat_[A-Za-z0-9_]{20,}\b"),
    re.compile(r"\b(?:AKIA|ASIA)[A-Z0-9]{16}\b"),
    re.compile(r"\bAIza[0-9A-Za-z_-]{35}\b"),
    re.compile(r"\bsk_(?:live|test)_[0-9A-Za-z]{16,}\b"),
    re.compile(r"\bsk-(?:proj-)?[0-9A-Za-z_-]{20,}\b"),
    re.compile(r"\bxox[baprs]-[0-9A-Za-z-]{10,}\b"),
    re.compile(r"[A-Za-z][A-Za-z0-9+.-]*://[^/\s:@]+:[^/\s@]+@"),
)
SECRET_ASSIGNMENT = re.compile(
    r"(?im)^\s*(?:api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|"
    r"secret|secret[_-]?key|token|password|passwd)"
    r"\s*[:=]\s*[\"']?([^\s\"'#;]{12,})"
)
SAFE_VALUE_MARKERS = (
    "changeme",
    "dummy",
    "example",
    "placeholder",
    "redacted",
    "sample",
    "your_",
)


def normalize_remote(remote: str) -> str:
    value = remote.strip().replace("\\", "/")
    if not value:
        return ""
    value = re.sub(r"^([^/@:\s]+)@([^:]+):", r"ssh://\1@\2/", value)
    if "://" not in value:
        return _local_remote(value, value)
    parsed = urlsplit(value)
    if parsed.scheme.casefold() == "file":
        return _local_remote(value, parsed.path)
    safe_netloc = parsed.netloc.rsplit("@", 1)[-1]
    path = re.sub(r"\.git$", "", parsed.path.rstrip("/"), flags=re.IGNORECASE)
    return urlunsplit((parsed.scheme.lower(), safe_netloc.lower(), path, "", ""))


def _local_remote(value: str, path: str) -> str:
    cleaned = re.sub(r"\.git$", "", path.rstrip("/"), flags=re.IGNORECASE)
    name = PurePosixPath(cleaned).name or "repository"
    safe_name = re.sub(r"[^a-zA-Z0-9._-]+", "-", name).strip("-_")
    digest = hashlib.sha256(os.path.normcase(value).encode("utf-8")).hexdigest()[:16]
    return f"local://{digest}/{safe_name or 'repository'}".lower()


def is_sensitive_path(relative: str) -> bool:
    path = PurePosixPath(relative)
    parts = {part.casefold() for part in path.parts}
    name = path.name.casefold()
    return bool(parts & (SKIP_PARTS | SECRET_DIRS)) or (
        name in SENSITIVE_NAMES
        or SENSITIVE_CONFIG.fullmatch(name) is not None
        or name.startswith(".env")
        or name.endswith(SENSITIVE_ENDINGS)
    )


def contains_high_confidence_secret(text: str) -> bool:
    if any(pattern.search(text) for pattern in SECRET_PATTERNS):
        return True
    for match in SECRET_ASSIGNMENT.finditer(text):
        value = match.group(1).strip()
        lowered = value.casefold()
        if value.startswith(("${", "{{", "<")):
            continue
        if any(marker in lowered for marker in SAFE_VALUE_MARKERS):
            continue
        if len(set(value)) >= 4:
            return True
    return False
