from __future__ import annotations

import json
import os
import shutil
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace


SCRIPTS = Path(__file__).resolve().parents[1]
DASHBOARD = SCRIPTS.parent / "dashboard"
sys.path[:0] = [str(SCRIPTS), str(DASHBOARD)]

from document_runtime import DocumentRuntime, inventory  # noqa: E402
from document_runtime_service import (  # noqa: E402
    check_document_runtime,
    install_document_runtime,
    prepare_document_runtime,
)
from knowledge_engine_models import (  # noqa: E402
    picture_classifier_paths,
    read_model_settings,
)


PACKAGES = {
    "docling": "2.121.0",
    "rapidocr": "3.9.2",
    "onnxruntime": "1.29.0",
    "paddleocr": "3.7.0",
    "paddlepaddle": "3.3.1",
    "transformers": "5.15.1",
    "torch": "2.13.0+cpu",
    "huggingface-hub": "1.28.0",
    "pypdfium2": "5.13.0",
    "Pillow": "12.3.0",
    "numpy": "2.3.5",
}


def _write(root: Path, relative: str, value: bytes = b"model") -> None:
    path = root / relative
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(value)


def _write_models(root: Path, duplicate: bool = False) -> None:
    base = "RapidOcr/onnx"
    _write(root, f"{base}/det/ch_PP-OCRv6_det_infer.onnx")
    _write(root, f"{base}/cls/ch_PP-OCRv4_cls_infer.onnx")
    _write(root, f"{base}/rec/ch_PP-OCRv6_rec_infer.onnx")
    _write(root, f"{base}/rec/ch_ppocrv6_keys.txt", b"keys")
    if duplicate:
        _write(root, f"{base}/det/ch_other_det_infer.onnx")


def _write_hf_model(root: Path, repo: str) -> None:
    if repo == "docling-project/docling-layout-heron-onnx":
        for name in ("config.json", "model.onnx", "preprocessor_config.json"):
            _write(root, name)
        return
    if repo == "docling-project/docling-models":
        base = "model_artifacts/tableformer"
        for mode in ("accurate", "fast"):
            _write(root, f"{base}/{mode}/tableformer_{mode}.safetensors")
            _write(root, f"{base}/{mode}/tm_config.json", b"{}")
        return
    if repo == "docling-project/DocumentFigureClassifier-v2.5":
        for name in ("config.json", "model.onnx", "preprocessor_config.json"):
            _write(root, name)
        return
    if repo.startswith("PaddlePaddle/"):
        _write(root, "config.json", b"{}")
        _write(root, "inference.json", b"{}")
        _write(root, "inference.pdiparams")
        _write(root, "inference.yml", b"model")
        return
    _write(root, "config.json", b"{}")
    _write(root, "preprocessor_config.json", b"{}")
    _write(root, "tokenizer.json", b"{}")
    _write(root, "model.safetensors")


class FakeRunner:
    def __init__(
        self,
        duplicate: bool = False,
        extra_distributions: tuple[str, ...] = (),
    ) -> None:
        self.calls: list[tuple[list[str], int]] = []
        self.duplicate = duplicate
        self.extra_distributions = extra_distributions
        self.fail_active = False

    def __call__(self, command: list[str], timeout: int) -> object:
        self.calls.append((list(command), timeout))
        if command[1:4] == ["-m", "venv", "--copies"]:
            venv = Path(command[-1])
            _write(venv, "bin/python", b"python")
            _write(venv, "bin/docling-tools", b"tool")
            _write(venv, "bin/hf", b"tool")
            _write(venv, "lib/python/site-packages/docling/runtime.py", b"ok")
        if Path(command[-1]).name == "document_runtime_probe.py":
            packages = dict(PACKAGES)
            if self.fail_active and "install-staging" not in command[0]:
                packages["docling"] = "0"
            payload = json.dumps({
                "packages": packages,
                "distributions": [
                    *packages,
                    *self.extra_distributions,
                ],
                "imports": list(packages),
                "onnx_providers": ["CPUExecutionProvider"],
            })
            output = f"YTQJK_RUNTIME_PROBE={payload}"
            return SimpleNamespace(returncode=0, stdout=output)
        if len(command) > 1 and Path(command[1]).name == (
            "document_runtime_assets.py"
        ):
            output = Path(command[command.index("--output") + 1])
            _write_models(output, self.duplicate)
        if len(command) > 2 and command[1] == "download":
            output = Path(command[command.index("--local-dir") + 1])
            _write_hf_model(output, command[2])
        return SimpleNamespace(returncode=0, stdout="")


class DocumentRuntimeTest(unittest.TestCase):
    def test_off_is_explicit_and_does_not_create_root(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "knowledge"
            receipt = prepare_document_runtime(root, "off")
            self.assertEqual(receipt["status"], "SKIPPED")
            self.assertEqual(receipt["runtime_status"], "NOT_CONFIGURED")
            self.assertFalse(root.exists())

    def test_install_is_selective_hashed_and_idempotent(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "knowledge"
            runner = FakeRunner()
            receipt = install_document_runtime(
                root, runner=runner, platform_name="linux"
            )
            self.assertEqual(receipt["status"], "READY")
            self.assertTrue(receipt["changed"])
            manifest_path = root / "models/document-intake/manifest.json"
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            self.assertEqual(
                set(manifest), {
                    "schema_version",
                    "files",
                    "rapidocr",
                    "paddleocr",
                    "smolvlm",
                    "ppstructure",
                }
            )
            classifier = "docling-project--DocumentFigureClassifier-v2.5"
            expected = {
                f"{classifier}/model.onnx",
                f"{classifier}/config.json",
                f"{classifier}/preprocessor_config.json",
            }
            self.assertTrue(expected.issubset(manifest["files"]))
            settings = read_model_settings(root)
            self.assertIsNotNone(settings)
            assert settings is not None
            self.assertEqual(settings.rapidocr, manifest["rapidocr"])
            self.assertEqual(
                set(settings.paddleocr),
                {
                    "text_detection_model_dir",
                    "text_recognition_model_dir",
                },
            )
            self.assertEqual(
                settings.smolvlm.name,
                "HuggingFaceTB--SmolVLM-256M-Instruct",
            )
            self.assertIsNotNone(settings.ppstructure)
            selected = picture_classifier_paths(
                settings.root,
                settings.files,
            )
            self.assertEqual(set(selected), {
                "model", "config", "preprocessor",
            })
            hf_downloads = [
                call for call, _ in runner.calls
                if len(call) > 2 and call[1] == "download"
            ]
            self.assertEqual(len(hf_downloads), 12)
            self.assertTrue(all("--revision" in call for call in hf_downloads))
            repos = {call[2] for call in hf_downloads}
            self.assertIn("PaddlePaddle/PP-OCRv6_medium_det", repos)
            self.assertIn("PaddlePaddle/PP-OCRv6_medium_rec", repos)
            repeated = prepare_document_runtime(
                root, runner=runner, platform_name="linux"
            )
            self.assertEqual(repeated["status"], "READY")
            self.assertFalse(repeated["changed"])
            current = [
                call for call, _ in runner.calls
                if len(call) > 2 and call[1] == "download"
            ]
            self.assertEqual(len(current), 12)
            json.dumps(repeated, ensure_ascii=False)

    def test_ambiguous_role_fails_without_active_runtime(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "knowledge"
            receipt = install_document_runtime(
                root, runner=FakeRunner(True), platform_name="linux"
            )
            self.assertEqual(receipt["status"], "FAILED")
            self.assertEqual(receipt["reason"], "MODEL_ROLE_AMBIGUOUS")
            self.assertFalse((root / "models/document-intake").exists())
            self.assertFalse(
                (root / ".runtime/document-intake/venv").exists()
            )

    def test_tamper_becomes_not_configured(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "knowledge"
            runner = FakeRunner()
            installed = install_document_runtime(
                root, runner=runner, platform_name="linux"
            )
            self.assertEqual(installed["status"], "READY")
            model = (
                root
                / "models/document-intake/"
                "docling-project--docling-layout-heron-onnx/model.onnx"
            )
            model.write_bytes(b"tampered")
            checked = check_document_runtime(
                root, runner=runner, platform_name="linux"
            )
            self.assertEqual(checked["status"], "NOT_CONFIGURED")
            self.assertEqual(checked["reason"], "MODEL_DIGEST_MISMATCH")

    def test_failed_active_readback_restores_previous_trees(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            root = base / "knowledge"
            requirements = base / "requirements.txt"
            source = SCRIPTS / "requirements-document.txt"
            shutil.copyfile(source, requirements)
            runner = FakeRunner()
            first = install_document_runtime(
                root,
                requirements=requirements,
                runner=runner,
                platform_name="linux",
            )
            self.assertEqual(first["status"], "READY")
            manager = DocumentRuntime(root, platform_name="linux")
            old_venv = inventory(manager.venv)
            old_models = inventory(manager.models)
            requirements.write_text(
                requirements.read_text(encoding="utf-8") + "\n",
                encoding="utf-8",
            )
            runner.fail_active = True
            failed = install_document_runtime(
                root,
                requirements=requirements,
                runner=runner,
                platform_name="linux",
            )
            self.assertEqual(failed["status"], "FAILED")
            self.assertEqual(failed["reason"], "PACKAGE_VERSION_MISMATCH")
            self.assertEqual(inventory(manager.venv), old_venv)
            self.assertEqual(inventory(manager.models), old_models)

    def test_windows_and_linux_runtime_paths(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            windows = DocumentRuntime(root, platform_name="win32")
            linux = DocumentRuntime(root, platform_name="linux")
            self.assertEqual(
                windows.python_path(root / "v"),
                root / "v/Scripts/python.exe",
            )
            self.assertEqual(
                linux.python_path(root / "v"), root / "v/bin/python"
            )

    def test_symlink_root_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            target = base / "target"
            target.mkdir()
            link = base / "link"
            try:
                os.symlink(target, link, target_is_directory=True)
            except OSError as error:
                self.skipTest(str(error))
            receipt = check_document_runtime(link, runner=FakeRunner())
            self.assertEqual(receipt["status"], "NOT_CONFIGURED")
            self.assertEqual(receipt["reason"], "UNSAFE_RUNTIME_PATH")


if __name__ == "__main__":
    unittest.main()
