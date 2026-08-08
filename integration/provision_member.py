"""Provision one disposable member for the pinned Compose integration stack."""

from __future__ import annotations

import asyncio
import os
from datetime import UTC, datetime, timedelta

from sqlalchemy import select

from app.config import get_settings
from app.db import create_engine, create_session_factory
from app.models.identity import Membership, Organization, User
from app.security import hash_password
from app.services.identity import issue_api_credential


async def main() -> tuple[str, str]:
    email = os.environ["ARTISAN_E2E_MEMBER_EMAIL"]
    nickname = os.environ["ARTISAN_E2E_MEMBER_NICKNAME"]
    password = os.environ["ARTISAN_E2E_MEMBER_PASSWORD"]
    slug = os.environ["ARTISAN_E2E_ORGANIZATION_SLUG"]
    settings = get_settings()
    engine = create_engine(settings.database_url)
    factory = create_session_factory(engine)
    try:
        async with factory() as session:
            organization = await session.scalar(
                select(Organization).where(Organization.slug == slug)
            )
            if organization is None:
                raise RuntimeError("disposable organization was not bootstrapped")
            user = User(
                email=email,
                normalized_email=email.casefold(),
                nickname=nickname,
                password_hash=hash_password(password),
            )
            session.add(user)
            await session.flush()
            session.add(
                Membership(
                    organization_id=organization.id,
                    user_id=user.id,
                    role="member",
                )
            )
            credential, token = await issue_api_credential(
                session,
                user=user,
                organization=organization,
                name="CLI member integration",
                expires_at=datetime.now(UTC) + timedelta(hours=1),
            )
            await session.commit()
            result = token, str(credential.id)
    finally:
        await engine.dispose()
    return result


if __name__ == "__main__":
    issued_token, issued_id = asyncio.run(main())
    print(f"{issued_token}\t{issued_id}")
