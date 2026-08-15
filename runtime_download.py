"""Download trusted runtime artifacts with Windows proxy compatibility."""
from __future__ import annotations

import platform
from pathlib import Path
import shutil
import subprocess
from typing import Callable
from urllib.parse import urlparse
from urllib.request import Request, urlopen

MAX_DOWNLOAD_BYTES = 256 * 1024 * 1024


def _validate_source(url: str) -> None:
    parsed = urlparse(url)
    if parsed.scheme != "https" or parsed.hostname != "nodejs.org":
        raise RuntimeError("runtime download source is not allowed")


def _download_with_curl(
    executable: str, url: str, destination: Path
) -> None:
    command = [
        executable,
        "--fail",
        "--location",
        "--silent",
        "--show-error",
        "--proto",
        "=https",
        "--proto-redir",
        "=https",
        "--connect-timeout",
        "20",
        "--max-time",
        "300",
        "--max-filesize",
        str(MAX_DOWNLOAD_BYTES),
        "--output",
        str(destination),
        url,
    ]
    try:
        subprocess.run(command, check=True, shell=False)
    except subprocess.CalledProcessError as error:
        raise RuntimeError("runtime download failed") from error


def _download_with_python(url: str, destination: Path) -> None:
    request = Request(url, headers={"User-Agent": "YTQJK installer"})
    size = 0
    with urlopen(request, timeout=60) as response:
        final = urlparse(response.geturl())
        if final.scheme != "https" or final.hostname != "nodejs.org":
            raise RuntimeError("runtime download redirect is not allowed")
        with destination.open("xb") as output:
            while chunk := response.read(64 * 1024):
                size += len(chunk)
                if size > MAX_DOWNLOAD_BYTES:
                    raise RuntimeError("runtime download exceeds size limit")
                output.write(chunk)


def download_node_file(
    url: str,
    destination: Path,
    *,
    system: str | None = None,
    which: Callable[[str], str | None] = shutil.which,
) -> None:
    _validate_source(url)
    current_system = system or platform.system()
    curl = which("curl.exe") if current_system == "Windows" else None
    if curl:
        _download_with_curl(curl, url, destination)
    else:
        _download_with_python(url, destination)
    if not destination.is_file():
        raise RuntimeError("runtime download did not create a file")
    if destination.stat().st_size > MAX_DOWNLOAD_BYTES:
        raise RuntimeError("runtime download exceeds size limit")
