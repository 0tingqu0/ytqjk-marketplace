"""HTTP adapter for dashboard release updates."""
from __future__ import annotations

import hmac
import json
from http import HTTPStatus
from typing import Any

from dashboard_update import (
    UpdateError,
    check_update,
    current_version,
    perform_update,
)


def send_update_status(handler: Any) -> None:
    """Send the version comparison and same-origin update token."""
    installed = ""
    try:
        installed = current_version(handler.plugin_root)
        result = check_update(handler.plugin_root)
        handler.send_json({**result, "token": handler.update_token})
    except UpdateError as error:
        handler.send_json(
            {
                "ok": False,
                "error": str(error),
                "current_version": installed,
                "token": handler.update_token,
            },
            HTTPStatus.BAD_GATEWAY,
        )


def handle_update_request(handler: Any) -> None:
    """Validate and serialize one dashboard-triggered update."""
    try:
        token = handler.read_payload().get("token")
    except (UnicodeDecodeError, json.JSONDecodeError, ValueError) as error:
        handler.send_json(
            {"ok": False, "error": str(error)}, HTTPStatus.BAD_REQUEST
        )
        return
    if not isinstance(token, str) or not hmac.compare_digest(
        token, handler.update_token
    ):
        handler.send_json(
            {
                "ok": False,
                "error": "更新请求无效。",
                "error_code": "UPDATE_TOKEN_INVALID",
            },
            HTTPStatus.BAD_REQUEST,
        )
        return
    if not handler.update_lock.acquire(blocking=False):
        handler.send_json(
            {"ok": False, "error": "更新正在进行。"}, HTTPStatus.CONFLICT
        )
        return
    try:
        handler.send_json({"ok": True, **perform_update(handler.plugin_root)})
    except UpdateError as error:
        handler.send_json(
            {"ok": False, "error": str(error)}, HTTPStatus.BAD_GATEWAY
        )
    finally:
        handler.update_lock.release()
