"""Bounded read-only inspection for the disposable roast-review integration."""

from __future__ import annotations

import asyncio
import json
import os
import re
import sys
import uuid
from pathlib import Path

from sqlalchemy import func, select

from app.config import get_settings
from app.db import create_engine, create_session_factory
from app.models.annotations import Comment
from app.models.archive import Roast
from app.models.identity import AuditEvent, Organization
from app.models.roast_reviews import RoastReviewComment

_MARKER_NAME = "ARTISAN_SERVER_E2E_DISPOSABLE"
_MARKER_VALUE = "artisan-server-e2e-compose-v1"
_FIXED_ORGANIZATION_SLUG = "my-roastery"
_PROJECT = re.compile(r"^artisan-server-e2e-[a-z0-9]{12}$")
_ROAST_UUID = re.compile(r"^[0-9a-f]{12}[1-8][0-9a-f]{3}[89ab][0-9a-f]{15}$")
_MAX_OUTPUT_BYTES = 16_384


def _pid_one_environment() -> dict[str, str]:
    raw = Path("/proc/1/environ").read_bytes()
    result: dict[str, str] = {}
    for entry in raw.split(b"\0"):
        if not entry:
            continue
        name, separator, value = entry.partition(b"=")
        if not separator:
            raise RuntimeError("malformed PID-1 environment")
        result[name.decode("utf-8", errors="strict")] = value.decode(
            "utf-8", errors="strict"
        )
    return result


def _require_disposable_target() -> uuid.UUID:
    if len(sys.argv) != 2 or not _ROAST_UUID.fullmatch(sys.argv[1]):
        raise RuntimeError("one canonical roast UUID argument is required")
    project = os.environ.get("ARTISAN_E2E_EXPECTED_PROJECT", "")
    if not _PROJECT.fullmatch(project):
        raise RuntimeError("invalid expected Compose project")
    if os.environ.get("COMPOSE_PROJECT_NAME") != project:
        raise RuntimeError("Compose project identity mismatch")
    if os.environ.get("ARTISAN_E2E_ORGANIZATION_SLUG") != _FIXED_ORGANIZATION_SLUG:
        raise RuntimeError("organization identity mismatch")
    pid_one = _pid_one_environment()
    if os.environ.get(_MARKER_NAME) != _MARKER_VALUE:
        raise RuntimeError("process disposable marker mismatch")
    if pid_one.get(_MARKER_NAME) != _MARKER_VALUE:
        raise RuntimeError("PID-1 disposable marker mismatch")
    return uuid.UUID(hex=sys.argv[1])


async def _inspect(roast_uuid: uuid.UUID) -> dict[str, object]:
    settings = get_settings()
    engine = create_engine(settings.database_url)
    factory = create_session_factory(engine)
    try:
        async with factory() as session:
            organization = await session.scalar(
                select(Organization).where(
                    Organization.slug == _FIXED_ORGANIZATION_SLUG
                )
            )
            if organization is None:
                raise RuntimeError("fixed disposable organization is absent")
            roast = await session.scalar(
                select(Roast).where(
                    Roast.organization_id == organization.id,
                    Roast.roast_uuid == roast_uuid,
                )
            )
            if roast is None:
                raise RuntimeError("disposable roast is absent")
            comment_ids = list(
                await session.scalars(
                    select(Comment.id)
                    .where(
                        Comment.organization_id == organization.id,
                        Comment.roast_id == roast.id,
                    )
                    .order_by(Comment.id)
                )
            )
            slot_comment_ids = list(
                await session.scalars(
                    select(RoastReviewComment.comment_id)
                    .where(
                        RoastReviewComment.organization_id == organization.id,
                        RoastReviewComment.roast_id == roast.id,
                    )
                    .order_by(RoastReviewComment.comment_id)
                )
            )
            audit_count = await session.scalar(
                select(func.count(AuditEvent.id)).where(
                    AuditEvent.organization_id == organization.id,
                    AuditEvent.event_type == "comment.created",
                    AuditEvent.subject_type == "comment",
                    AuditEvent.subject_id.in_(comment_ids),
                )
            )
            return {
                "audit_count": int(audit_count or 0),
                "comment_count": len(comment_ids),
                "comment_ids": [value.hex for value in comment_ids],
                "slot_comment_ids": [value.hex for value in slot_comment_ids],
                "slot_count": len(slot_comment_ids),
            }
    finally:
        await engine.dispose()


def main() -> None:
    roast_uuid = _require_disposable_target()
    encoded = json.dumps(
        asyncio.run(_inspect(roast_uuid)),
        ensure_ascii=True,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("ascii")
    if len(encoded) > _MAX_OUTPUT_BYTES:
        raise RuntimeError("inspection output exceeded bound")
    sys.stdout.buffer.write(encoded + b"\n")


if __name__ == "__main__":
    main()
