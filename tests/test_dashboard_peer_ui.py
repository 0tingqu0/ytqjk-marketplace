from __future__ import annotations

from html.parser import HTMLParser
from pathlib import Path
import re


ROOT = Path(__file__).resolve().parents[1]
DASHBOARD = ROOT / "plugins/ytqjk-agentic-orchestrator/skills/ytqjk/dashboard"


class IdParser(HTMLParser):
    def __init__(self) -> None:
        super().__init__()
        self.elements: dict[str, tuple[str, dict[str, str | None]]] = {}

    def handle_starttag(
        self,
        tag: str,
        attrs: list[tuple[str, str | None]],
    ) -> None:
        values = dict(attrs)
        identifier = values.get("id")
        if identifier:
            self.elements[identifier] = (tag, values)


def _dom() -> IdParser:
    parser = IdParser()
    parser.feed((DASHBOARD / "index.html").read_text(encoding="utf-8"))
    return parser


def test_peer_workspace_has_complete_accessible_controls() -> None:
    dom = _dom().elements
    expected = {
        "peer-state",
        "peer-runtime",
        "peer-bootstrap",
        "peer-service-form",
        "peer-bind-host",
        "peer-port",
        "peer-service-enabled",
        "peer-service-insecure",
        "peer-new",
        "peer-secret",
        "peer-list",
        "peer-dispatch-form",
        "peer-dispatch-project",
        "peer-dispatch-query",
        "peer-dispatch-limit",
        "peer-results",
        "peer-remote-node-id",
        "peer-export-node-ids",
        "peer-discover",
        "peer-dialog",
        "peer-secret-dialog",
        "peer-material-dialog",
    }
    assert expected <= dom.keys()
    assert dom["peer-dialog"][1]["aria-labelledby"]
    assert dom["peer-secret-dialog"][1]["aria-labelledby"]
    assert dom["peer-material-dialog"][1]["aria-labelledby"]
    assert dom["peer-shared-secret"][1]["type"] == "password"
    assert dom["peer-shared-secret"][1]["autocomplete"] == "new-password"
    assert dom["peer-status"][1]["role"] == "status"
    assert dom["peer-project-id"][0] == "select"
    assert dom["peer-dispatch-project"][0] == "select"
    assert dom["peer-remote-node-id"][0] == "select"
    assert dom["peer-export-node-ids"][0] == "select"
    assert "multiple" in dom["peer-export-node-ids"][1]
    assert "disabled" in dom["peer-remote-node-id"][1]


def test_every_peer_javascript_dom_reference_exists() -> None:
    html_ids = _dom().elements.keys()
    references: set[str] = set()
    for path in (DASHBOARD / "js/peers").glob("*.js"):
        source = path.read_text(encoding="utf-8")
        references.update(re.findall(r'byId\("([^"]+)"\)', source))
    assert references <= html_ids


def test_peer_contract_fields_and_material_scope_are_explicit() -> None:
    control = (DASHBOARD / "js/peers/control.js").read_text(
        encoding="utf-8"
    )
    form = (DASHBOARD / "js/peers/form.js").read_text(encoding="utf-8")
    peer_source = control + form
    api = (DASHBOARD / "js/api.js").read_text(encoding="utf-8")
    for field in (
        "project_id",
        "endpoint",
        "remote_node_id",
        "export_node_ids",
        "allow_insecure",
        "enabled",
    ):
        assert field in peer_source
    assert "remote_library_node: row.library_node" in control
    for action in (
        "bootstrap",
        "configure",
        "discover",
        "secret",
        "upsert",
        "remove",
        "health",
        "dispatch",
        "material",
    ):
        assert f'/api/peers/{action}' in api


def test_project_and_node_selects_show_names_and_submit_ids() -> None:
    render = (DASHBOARD / "js/peers/render.js").read_text(
        encoding="utf-8"
    )
    assert "option.value = project.id" in render
    assert "option.textContent = project.name || project.id" in render
    assert 'renderProjectSelect("peer-project-id"' in render
    assert 'renderProjectSelect("peer-dispatch-project"' in render
    assert "option.value = node.id" in render
    assert "option.textContent = node.title || node.id" in render
    assert 'renderNodeSelect("peer-remote-node-id"' in render
    assert '"peer-export-node-ids"' in render
    assert "state.peerRemoteLibraries" in render


def test_saved_secret_is_not_rendered_or_persisted() -> None:
    render = (DASHBOARD / "js/peers/render.js").read_text(
        encoding="utf-8"
    )
    control = (DASHBOARD / "js/peers/control.js").read_text(
        encoding="utf-8"
    )
    form = (DASHBOARD / "js/peers/form.js").read_text(encoding="utf-8")
    peer_source = control + form
    assert "peer.secret" not in render
    assert "key_fingerprint" in render
    assert "localStorage" not in peer_source
    assert 'field("peer-secret-value", "")' in control
    assert 'field("peer-shared-secret", "")' in peer_source
    assert "secret: secret || null" in form
    assert 'required = !record' in form
    assert "留空会保留原值" in form


def test_peer_ui_is_mobile_safe_and_files_stay_bounded() -> None:
    html = (DASHBOARD / "index.html").read_text(encoding="utf-8")
    app = (DASHBOARD / "app.js").read_text(encoding="utf-8")
    peer_css = (DASHBOARD / "peer.css").read_text(encoding="utf-8")
    assert 'value="query-v1"' in html
    assert "min-height: 44px" in peer_css
    assert "grid-template-columns: 1fr" in peer_css
    assert "overflow-wrap: anywhere" in peer_css
    assert len(html.splitlines()) <= 200
    assert len(app.splitlines()) <= 200
    for path in (DASHBOARD / "js/peers").glob("*.js"):
        assert len(path.read_text(encoding="utf-8").splitlines()) <= 300


def test_peer_dialogs_close_on_backdrop() -> None:
    control = (DASHBOARD / "js/peers/control.js").read_text(
        encoding="utf-8"
    )
    assert "event.target !== event.currentTarget" in control
    assert 'id === "peer-secret-dialog"' in control
