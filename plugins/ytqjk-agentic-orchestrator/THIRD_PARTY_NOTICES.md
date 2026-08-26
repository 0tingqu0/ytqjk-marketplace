# Third-party notices

## Caveman skill

YTQJK bundles an adapted Codex-compatible copy of Matt Pocock's historical
`caveman` skill. Audited upstream snapshot:
<https://github.com/mattpocock/skills/tree/221ffca/skills/productivity/caveman>
The behavior text is preserved; frontmatter and attribution were adapted for
this plugin. Upstream removed `caveman` after that revision, so YTQJK carries
the audited snapshot instead of depending on a moving install target.

MIT License

Copyright (c) 2026 Matt Pocock

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

## Local document runtime

The optional document-intake runtime installs pinned direct upstream packages
and downloads pinned official model revisions into an isolated local
directory. Transitive package versions are recorded and the complete installed
tree is integrity sealed; they are not represented as a cross-platform lock.
Package license files shipped in distribution metadata remain in that isolated
runtime. Those components remain governed by their upstream licenses:

- [Docling](https://github.com/docling-project/docling) is licensed under the
  MIT License.
- [RapidOCR](https://github.com/RapidAI/RapidOCR) is licensed under the
  Apache License 2.0.
- [PaddleOCR](https://github.com/PaddlePaddle/PaddleOCR), including the
  PP-OCRv6 detection and recognition models used here, is licensed under the
  Apache License 2.0.
- [PaddlePaddle](https://github.com/PaddlePaddle/Paddle) is licensed under the
  Apache License 2.0.
- [ONNX Runtime](https://github.com/microsoft/onnxruntime) is licensed under
  the MIT License. YTQJK installs the CPU package, not `onnxruntime-gpu`.
- [Transformers](https://github.com/huggingface/transformers) and
  [huggingface_hub](https://github.com/huggingface/huggingface_hub) are
  licensed under the Apache License 2.0.
- [PyTorch](https://github.com/pytorch/pytorch) is a multi-license
  distribution. Its package metadata lists Apache-2.0, LLVM-exception,
  BSD-2-Clause, BSD-3-Clause, BSL-1.0, and MIT components. See the upstream
  `LICENSE` and `NOTICE` files shipped with the installed package.
- [pypdfium2](https://github.com/pypdfium2-team/pypdfium2) is distributed
  under BSD-3-Clause and Apache-2.0 terms, with dependency notices.
- [Pillow](https://github.com/python-pillow/Pillow) is licensed under the
  MIT-CMU license.
- [NumPy](https://github.com/numpy/numpy) is licensed under BSD-3-Clause and
  ships additional notices for bundled binary dependencies.
- SmolVLM-256M-Instruct is published by Hugging Face under the Apache License
  2.0. Official model card:
  <https://hf.co/HuggingFaceTB/SmolVLM-256M-Instruct>
- DocumentFigureClassifier-v2.5 is published by the Docling project under the
  MIT License. The former `ds4sd` namespace redirects to the official
  `docling-project` namespace. Official model card:
  <https://hf.co/docling-project/DocumentFigureClassifier-v2.5>
- Docling Layout Heron ONNX is published under Apache-2.0:
  <https://hf.co/docling-project/docling-layout-heron-onnx>
- Docling TableFormer assets are published under CDLA-Permissive-2.0 and
  Apache-2.0:
  <https://hf.co/docling-project/docling-models>
- PP-DocLayout, PP-OCRv6, PP-LCNet, SLANeXt, SLANet, and RT-DETR table-cell
  models are published by PaddlePaddle under Apache-2.0. YTQJK downloads only
  the fixed revisions declared in `document_runtime_downloads.py`.

YTQJK does not relicense these packages or model weights. Their copyright,
model-card limitations, notices, and license texts remain with the respective
upstream authors and distributions.
