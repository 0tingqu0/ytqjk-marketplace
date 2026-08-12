from __future__ import annotations

import re
import struct
import wave
import zipfile
from io import BytesIO
from pathlib import Path
from xml.etree import ElementTree


TEXT_EXTENSIONS = {
    ".adoc", ".asciidoc", ".bat", ".bib", ".c", ".cc", ".cfg", ".clj", ".cljc", ".cljs",
    ".cmake", ".coffee", ".conf", ".cpp", ".cs", ".css", ".csv", ".dart", ".diff", ".dockerfile",
    ".elm", ".env.example", ".ex", ".exs", ".fish", ".fs", ".fsx", ".gitattributes", ".gitignore",
    ".go", ".gradle", ".graphql", ".groovy", ".h", ".hcl", ".hpp", ".html", ".ini", ".jade", ".java",
    ".jl", ".js", ".json", ".jsonl", ".jsx", ".kt", ".kts", ".less", ".liquid", ".log", ".lua",
    ".md", ".mdx", ".mjs", ".mk", ".nim", ".nix", ".php", ".pl", ".pm", ".properties", ".proto",
    ".prisma", ".ps1", ".psd1", ".pug", ".py", ".r", ".rake", ".rb", ".rst", ".rs", ".sass",
    ".sbt", ".scss", ".sh", ".sol", ".sql", ".styl", ".svg", ".swift", ".tf", ".toml", ".ts",
    ".tsx", ".txt", ".v", ".vala", ".vim", ".vue", ".xaml", ".xml", ".yaml", ".yml", ".zsh",
}
TEXT_FILE_NAMES = {"dockerfile", "license", "makefile", "readme"}
OFFICE_EXTENSIONS = {".docx", ".pptx", ".xlsx"}
IMAGE_EXTENSIONS = {".gif", ".jpeg", ".jpg", ".png", ".webp"}
AUDIO_EXTENSIONS = {".aac", ".flac", ".m4a", ".mp3", ".ogg", ".opus", ".wav"}
SUPPORTED_EXTENSIONS = TEXT_EXTENSIONS | OFFICE_EXTENSIONS | IMAGE_EXTENSIONS | AUDIO_EXTENSIONS
MAX_ARCHIVE_FILES = 512
MAX_ARCHIVE_EXPANDED_BYTES = 20 * 1024 * 1024


def extract_upload(name: str, source: bytes) -> tuple[str, dict[str, object]]:
    extension = supported_extension(name)
    if extension in TEXT_EXTENSIONS:
        return _decode_text(source), {}
    if extension == ".docx":
        return _docx_text(source), {}
    if extension == ".pptx":
        return _pptx_text(source), {}
    if extension == ".xlsx":
        return _xlsx_text(source), {}
    if extension in IMAGE_EXTENSIONS:
        width, height = image_dimensions(source, extension)
        return "", {"dimensions": f"{width} x {height}" if width else "无法识别"}
    if extension in AUDIO_EXTENSIONS:
        return "", {"audio": audio_metadata(source, extension)}
    raise ValueError("不支持的资料格式。")


def supported_extension(name: str) -> str:
    lowered = name.lower()
    if lowered in TEXT_FILE_NAMES:
        return ".txt"
    return ".env.example" if lowered.endswith(".env.example") else Path(lowered).suffix


def _decode_text(source: bytes) -> str:
    for encoding in _text_encodings(source):
        try:
            text = source.decode(encoding)
        except UnicodeDecodeError:
            continue
        if _is_text_content(text):
            return text.lstrip("\ufeff")
    raise ValueError("无法识别文本编码；支持 UTF-8、UTF-16、UTF-32、GB18030、Big5、Shift_JIS 和 EUC-KR。")


def _text_encodings(source: bytes) -> tuple[str, ...]:
    if source.startswith((b"\xff\xfe\x00\x00", b"\x00\x00\xfe\xff")):
        return ("utf-32",)
    if source.startswith((b"\xff\xfe", b"\xfe\xff")):
        return ("utf-16",)
    return ("utf-8-sig", "gb18030", "big5", "shift_jis", "euc_kr")


def _is_text_content(text: str) -> bool:
    if not text:
        return True
    controls = sum(ord(character) < 32 and character not in "\n\r\t" for character in text)
    return controls * 100 <= len(text)


def _open_archive(source: bytes) -> zipfile.ZipFile:
    try:
        archive = zipfile.ZipFile(__import__("io").BytesIO(source))
    except zipfile.BadZipFile as exc:
        raise ValueError("Office 文件损坏或不是有效的现代 Office 格式。") from exc
    files = archive.infolist()
    if len(files) > MAX_ARCHIVE_FILES or sum(item.file_size for item in files) > MAX_ARCHIVE_EXPANDED_BYTES:
        archive.close()
        raise ValueError("Office 文件展开后过大，未分析。")
    return archive


def _xml_text(source: bytes) -> list[str]:
    try:
        root = ElementTree.fromstring(source)
    except ElementTree.ParseError as exc:
        raise ValueError("Office 文件中的 XML 内容无效。") from exc
    return [node.text.strip() for node in root.iter() if node.text and node.text.strip()]


def _docx_text(source: bytes) -> str:
    with _open_archive(source) as archive:
        try:
            parts = _xml_text(archive.read("word/document.xml"))
        except KeyError as exc:
            raise ValueError("Word 文件缺少正文。") from exc
    return "\n".join(parts)


def _pptx_text(source: bytes) -> str:
    with _open_archive(source) as archive:
        slides = sorted(
            (name for name in archive.namelist() if re.fullmatch(r"ppt/slides/slide\d+\.xml", name)),
            key=lambda value: int(re.search(r"\d+", value).group()),
        )
        if not slides:
            raise ValueError("PowerPoint 文件没有可读取的幻灯片。")
        groups = [f"第 {index} 页\n" + "\n".join(_xml_text(archive.read(slide))) for index, slide in enumerate(slides, 1)]
    return "\n\n".join(groups)


def _xlsx_text(source: bytes) -> str:
    with _open_archive(source) as archive:
        shared = _shared_strings(archive)
        sheets = sorted(
            (name for name in archive.namelist() if re.fullmatch(r"xl/worksheets/sheet\d+\.xml", name)),
            key=lambda value: int(re.search(r"\d+", value).group()),
        )
        if not sheets:
            raise ValueError("Excel 文件没有可读取的工作表。")
        groups = [_sheet_text(archive.read(sheet), shared, index) for index, sheet in enumerate(sheets, 1)]
    return "\n\n".join(groups)


def _shared_strings(archive: zipfile.ZipFile) -> list[str]:
    try:
        root = ElementTree.fromstring(archive.read("xl/sharedStrings.xml"))
    except KeyError:
        return []
    except ElementTree.ParseError as exc:
        raise ValueError("Excel 共享文本无效。") from exc
    return ["".join(node.itertext()).strip() for node in root if node.tag.rsplit("}", 1)[-1] == "si"]


def _sheet_text(source: bytes, shared: list[str], index: int) -> str:
    try:
        root = ElementTree.fromstring(source)
    except ElementTree.ParseError as exc:
        raise ValueError("Excel 工作表内容无效。") from exc
    rows = []
    for row in (node for node in root.iter() if node.tag.rsplit("}", 1)[-1] == "row"):
        cells = []
        for cell in (node for node in row if node.tag.rsplit("}", 1)[-1] == "c"):
            value = next((child.text for child in cell if child.tag.rsplit("}", 1)[-1] == "v"), "") or ""
            kind = cell.attrib.get("t")
            if kind == "s" and value.isdigit() and int(value) < len(shared):
                value = shared[int(value)]
            elif kind == "inlineStr":
                value = "".join(cell.itertext()).strip()
            cells.append(value)
        if any(cells):
            rows.append(" | ".join(cells))
    return f"工作表 {index}\n" + "\n".join(rows)


def image_dimensions(source: bytes, extension: str) -> tuple[int, int]:
    if extension == ".png" and source.startswith(b"\x89PNG\r\n\x1a\n") and len(source) >= 24:
        return struct.unpack(">II", source[16:24])
    if extension == ".gif" and source[:6] in {b"GIF87a", b"GIF89a"} and len(source) >= 10:
        return struct.unpack("<HH", source[6:10])
    if extension in {".jpg", ".jpeg"}:
        return _jpeg_dimensions(source)
    if extension == ".webp" and source[:4] == b"RIFF" and source[8:12] == b"WEBP":
        return _webp_dimensions(source)
    return 0, 0


def _jpeg_dimensions(source: bytes) -> tuple[int, int]:
    position = 2
    while position + 9 < len(source) and source[:2] == b"\xff\xd8":
        if source[position] != 0xFF:
            position += 1
            continue
        marker = source[position + 1]
        length = int.from_bytes(source[position + 2:position + 4], "big")
        if 0xC0 <= marker <= 0xC3 and length >= 7:
            height, width = struct.unpack(">HH", source[position + 5:position + 9])
            return width, height
        position += 2 + length
    return 0, 0


def _webp_dimensions(source: bytes) -> tuple[int, int]:
    if source[12:16] == b"VP8X" and len(source) >= 30:
        width = int.from_bytes(source[24:27], "little") + 1
        height = int.from_bytes(source[27:30], "little") + 1
        return width, height
    return 0, 0


def audio_metadata(source: bytes, extension: str) -> str:
    label = extension.removeprefix(".").upper()
    if extension != ".wav":
        return f"{label}，未提取语音文本"
    try:
        with wave.open(BytesIO(source)) as audio:
            duration = audio.getnframes() / audio.getframerate()
            return f"WAV，{audio.getnchannels()} 声道，{audio.getframerate()} Hz，{duration:.1f} 秒，未提取语音文本"
    except (EOFError, wave.Error):
        return "WAV，元数据无法识别，未提取语音文本"
