"""WebSocket endpoint for real-time UI updates.

Maintains per-project connection sets and provides a broadcast function
that other modules (routers, orchestrator) can import and call.
"""

from __future__ import annotations

import asyncio
import logging
import uuid
from typing import Any

from fastapi import APIRouter, WebSocket, WebSocketDisconnect
from starlette.websockets import WebSocketState

logger = logging.getLogger(__name__)

router = APIRouter()

# Per-project WebSocket connection sets: project_id -> set of WebSocket connections
_connections: dict[uuid.UUID, set[WebSocket]] = {}


def _get_connections(project_id: uuid.UUID) -> set[WebSocket]:
    """Return the connection set for a project, creating if needed."""
    if project_id not in _connections:
        _connections[project_id] = set()
    return _connections[project_id]


async def broadcast(project_id: uuid.UUID, event: dict[str, Any]) -> None:
    """Send a JSON event to all WebSocket clients subscribed to a project.

    Silently removes dead connections.
    """
    connections = _get_connections(project_id)
    if not connections:
        return

    dead: list[WebSocket] = []
    tasks = []
    for ws in connections:
        if ws.client_state == WebSocketState.CONNECTED:
            tasks.append(_safe_send(ws, event, dead))
        else:
            dead.append(ws)

    if tasks:
        await asyncio.gather(*tasks)

    for ws in dead:
        connections.discard(ws)


async def _safe_send(
    ws: WebSocket, event: dict[str, Any], dead: list[WebSocket]
) -> None:
    """Send JSON to a websocket, catching errors and marking dead sockets."""
    try:
        await ws.send_json(event)
    except Exception:
        dead.append(ws)


@router.websocket("/api/ws/{project_id}")
async def ws_endpoint(websocket: WebSocket, project_id: uuid.UUID) -> None:
    """WebSocket endpoint per project.

    On connect: client is added to the project's connection set.
    On disconnect: client is removed.
    """
    await websocket.accept()
    connections = _get_connections(project_id)
    connections.add(websocket)
    logger.info("WebSocket connected for project %s (total: %d)", project_id, len(connections))

    try:
        while True:
            # Keep the connection alive; we don't expect messages from the client
            # but we need to read to detect disconnects.
            await websocket.receive_text()
    except WebSocketDisconnect:
        pass
    finally:
        connections.discard(websocket)
        logger.info(
            "WebSocket disconnected for project %s (remaining: %d)",
            project_id,
            len(connections),
        )
