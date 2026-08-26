from __future__ import annotations

import multiprocessing
import os
import sys
from pathlib import Path


TESTS = Path(__file__).resolve().parent
SCRIPTS = TESTS.parent
DASHBOARD = SCRIPTS.parent / "dashboard"
sys.path[:0] = [str(TESTS), str(SCRIPTS), str(DASHBOARD)]

from document_runtime_lock import RuntimeInstallLock  # noqa: E402
from document_runtime_service import install_document_runtime  # noqa: E402
from test_document_runtime import FakeRunner  # noqa: E402


def _hold_lock(
    path: Path,
    ready: multiprocessing.synchronize.Event,
    release: multiprocessing.synchronize.Event,
) -> None:
    with RuntimeInstallLock(path, 5.0):
        ready.set()
        release.wait(10.0)


def _crash_with_lock(
    path: Path,
    ready: multiprocessing.synchronize.Event,
) -> None:
    lock = RuntimeInstallLock(path, 5.0)
    lock.__enter__()
    ready.set()
    os._exit(17)


def test_install_times_out_while_another_process_holds_lock(
    tmp_path: Path,
) -> None:
    runtime = tmp_path / "knowledge/.runtime/document-intake"
    runtime.mkdir(parents=True)
    lock_path = runtime / ".install.lock"
    context = multiprocessing.get_context("spawn")
    ready = context.Event()
    release = context.Event()
    process = context.Process(
        target=_hold_lock,
        args=(lock_path, ready, release),
    )
    process.start()
    try:
        assert ready.wait(10.0)
        receipt = install_document_runtime(
            tmp_path / "knowledge",
            runner=FakeRunner(),
            platform_name="linux",
            lock_timeout_seconds=0.1,
        )
        assert receipt["status"] == "FAILED"
        assert receipt["reason"] == "RUNTIME_INSTALL_LOCK_TIMEOUT"
        staging = runtime / "install-staging"
        assert not staging.exists()
    finally:
        release.set()
        process.join(10.0)
        if process.is_alive():
            process.terminate()
            process.join(5.0)
    assert process.exitcode == 0


def test_kernel_releases_lock_after_process_crash(tmp_path: Path) -> None:
    runtime = tmp_path / "runtime"
    runtime.mkdir()
    lock_path = runtime / ".install.lock"
    context = multiprocessing.get_context("spawn")
    ready = context.Event()
    process = context.Process(
        target=_crash_with_lock,
        args=(lock_path, ready),
    )
    process.start()
    assert ready.wait(10.0)
    process.join(10.0)
    if process.is_alive():
        process.terminate()
        process.join(5.0)
    assert process.exitcode == 17

    with RuntimeInstallLock(lock_path, 1.0):
        assert lock_path.is_file()
