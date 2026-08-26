from __future__ import annotations

import concurrent.futures
import hashlib
import json
import subprocess
import sys
import threading
import time
from pathlib import Path

import pytest


TESTS = Path(__file__).resolve().parent
sys.path.insert(0, str(TESTS))

from test_structured_candidate_lifecycle import (  # noqa: E402
    CANDIDATE_ID,
    _create_bundle,
    _relative,
)

import approval_promotion  # noqa: E402
import candidate_actions  # noqa: E402
import candidate_file_safety  # noqa: E402
from candidate_bundle import candidate_lifecycle_lock  # noqa: E402


PROCESS_PROMOTE = """
import sys
from pathlib import Path
sys.path[:0] = [sys.argv[1], sys.argv[2]]
from approval_promotion import promote
root = Path(sys.argv[3])
print(promote(root, Path(sys.argv[4]), require_ready=False), flush=True)
"""


@pytest.mark.parametrize("source", [None, "external"])
@pytest.mark.parametrize("action", ["approve", "delete", "edit"])
def test_structured_namespace_blocks_invalid_source(
    tmp_path: Path, source: str | None, action: str,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    assert isinstance(document, Path)
    content = document.read_text(encoding="utf-8")
    replacement = "" if source is None else f"source: {source}\n"
    content = content.replace("source: structured-intake\n", replacement)
    document.write_text(content, encoding="utf-8")
    raw = _relative(tmp_path, document)

    if action == "approve":
        assert not approval_promotion.promote(
            tmp_path, document, require_ready=False,
        )
    else:
        with pytest.raises(ValueError, match="STRUCTURED_SOURCE_INVALID"):
            if action == "delete":
                candidate_actions.delete_candidate(tmp_path, raw)
            else:
                candidate_actions.update_candidate(
                    tmp_path, raw, content + "body\n",
                )
    assert document.read_text(encoding="utf-8") == content
    assert all(
        isinstance(bundle[key], Path) and bundle[key].exists()
        for key in ("detail", "original", "chunk")
    )


@pytest.mark.parametrize("artifact", ["detail", "chunk", "original"])
def test_secret_in_any_structured_artifact_blocks_all_moves(
    tmp_path: Path, artifact: str,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    assert isinstance(document, Path)
    secret = b"api_key=ABCDEFGHIJKLMNOP"
    target = bundle[artifact]
    assert isinstance(target, Path)
    if artifact == "detail":
        value = json.loads(target.read_text(encoding="utf-8"))
        value["metadata"]["secret"] = secret.decode()
        target.write_text(json.dumps(value), encoding="utf-8")
    elif artifact == "chunk":
        target.write_bytes(secret)
    else:
        target.write_bytes(secret)
        digest = hashlib.sha256(secret).hexdigest()
        content = document.read_text(encoding="utf-8").replace(
            bundle_digest(bundle), digest,
        )
        document.write_text(content, encoding="utf-8")
        detail = bundle["detail"]
        assert isinstance(detail, Path)
        value = json.loads(detail.read_text(encoding="utf-8"))
        value["source_digest"] = digest
        detail.write_text(json.dumps(value), encoding="utf-8")

    assert not approval_promotion.promote(
        tmp_path, document, require_ready=False,
    )
    assert document.exists()
    approved = tmp_path / "personal-experience" / "approved"
    assert not approved.exists() or not list(approved.rglob("*.*"))


def bundle_digest(bundle: dict[str, Path | str | bytes]) -> str:
    document = bundle["document"]
    assert isinstance(document, Path)
    for line in document.read_text(encoding="utf-8").splitlines():
        if line.startswith("source_sha256: "):
            return line.partition(": ")[2]
    raise AssertionError("digest missing")


def assert_bundle_state(
    bundle: dict[str, Path | str | bytes], approved: bool,
) -> None:
    for key in ("document", "detail", "original", "chunks"):
        path = bundle[key]
        assert isinstance(path, Path)
        target = Path(str(path).replace("candidates", "approved", 1))
        assert not path.exists()
        assert target.exists() is approved


@pytest.mark.parametrize(
    ("first", "second", "approved"),
    [
        ("approve", "delete", True),
        ("edit", "delete", False),
        ("approve", "approve", True),
    ],
)
def test_threaded_candidate_operations_are_serialized(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    first: str,
    second: str,
    approved: bool,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    content = bundle["content"]
    assert isinstance(document, Path) and isinstance(content, str)
    entered = threading.Event()
    release = threading.Event()
    module = (
        approval_promotion if first == "approve" else candidate_actions
    )
    original = module.load_structured_bundle

    def delayed(*args: object) -> object:
        entered.set()
        assert release.wait(2)
        return original(*args)

    monkeypatch.setattr(module, "load_structured_bundle", delayed)
    raw = _relative(tmp_path, document)

    def run(action: str) -> bool:
        try:
            if action == "approve":
                return approval_promotion.promote(
                    tmp_path, document, require_ready=False,
                )
            if action == "edit":
                candidate_actions.update_candidate(
                    tmp_path, raw, content.replace("Body", "Edited"),
                )
            else:
                candidate_actions.delete_candidate(tmp_path, raw)
            return True
        except ValueError:
            return False

    with concurrent.futures.ThreadPoolExecutor(2) as pool:
        first_result = pool.submit(run, first)
        assert entered.wait(2)
        second_result = pool.submit(run, second)
        time.sleep(0.1)
        assert not second_result.done()
        release.set()
        results = (first_result.result(3), second_result.result(3))
    assert results[0]
    assert_bundle_state(bundle, approved)


def test_failed_approve_rolls_back_before_waiting_delete(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    assert isinstance(document, Path)
    entered = threading.Event()
    release = threading.Event()
    original_write = approval_promotion._commit_write
    failed = False

    def fail_once(path: Path, content: bytes) -> None:
        nonlocal failed
        if not failed:
            failed = True
            entered.set()
            assert release.wait(2)
            raise OSError("injected")
        original_write(path, content)

    def delete() -> bool:
        candidate_actions.delete_candidate(
            tmp_path, _relative(tmp_path, document),
        )
        return True

    monkeypatch.setattr(approval_promotion, "_commit_write", fail_once)
    with concurrent.futures.ThreadPoolExecutor(2) as pool:
        approval = pool.submit(
            approval_promotion.promote,
            tmp_path,
            document,
            require_ready=False,
        )
        assert entered.wait(2)
        deletion = pool.submit(delete)
        time.sleep(0.1)
        assert not deletion.done()
        release.set()
        assert not approval.result(3)
        assert deletion.result(3)
    assert_bundle_state(bundle, False)


def test_process_uses_same_candidate_lock(tmp_path: Path) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    assert isinstance(document, Path)
    dashboard = TESTS.parents[1] / "dashboard"
    scripts = dashboard.parent / "scripts"
    command = [
        sys.executable, "-X", "utf8", "-c", PROCESS_PROMOTE,
        str(dashboard), str(scripts), str(tmp_path), str(document),
    ]
    process: subprocess.Popen[str] | None = None
    try:
        with candidate_lifecycle_lock(tmp_path, document):
            process = subprocess.Popen(
                command, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                text=True, encoding="utf-8",
            )
            time.sleep(0.2)
            assert process.poll() is None
        output, error = process.communicate(timeout=5)
    finally:
        if process is not None and process.poll() is None:
            process.kill()
    assert process is not None and process.returncode == 0, error
    assert output.strip() == "True"
    assert_bundle_state(bundle, True)


def test_external_path_cannot_create_candidate_lock(tmp_path: Path) -> None:
    outside = tmp_path.parent / f"outside-{CANDIDATE_ID}.md"
    with pytest.raises(ValueError, match="CANDIDATE_LOCK_FAILED"):
        with candidate_lifecycle_lock(tmp_path, outside):
            raise AssertionError("external lock unexpectedly acquired")
    assert not (tmp_path / ".locks").exists()


def test_reparse_candidate_cannot_create_lock(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    bundle = _create_bundle(tmp_path)
    document = bundle["document"]
    assert isinstance(document, Path)
    real_check = candidate_file_safety.is_reparse
    monkeypatch.setattr(
        candidate_file_safety,
        "is_reparse",
        lambda path: path == document or real_check(path),
    )
    with pytest.raises(ValueError, match="CANDIDATE_LOCK_FAILED"):
        with candidate_lifecycle_lock(tmp_path, document):
            raise AssertionError("reparse lock unexpectedly acquired")
    assert not (tmp_path / ".locks").exists()
