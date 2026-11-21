"""Anona Runners: read GitHub Actions runner installations and jobs.

Beta: Anona Runners may change.

Gateway contract (Anona routes, not E2B): ``GET /v1/runners/installations``,
``POST /v1/runners/installations/link``, ``GET /v1/runners/jobs`` and
``GET /v1/runners/jobs/{id}``. Responses are camelCase; the dataclasses here use
snake_case. A ``401`` raises ``AuthenticationException``; ``403
runners_not_eligible`` raises the same type carrying the API's message.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional
from urllib.parse import quote, urlencode

from e2b.connection_config import ConnectionConfig

from anona_compute._config import apply_env
from anona_compute._http import get_json, post_json


@dataclass
class Installation:
    id: int
    account_login: str
    account_type: str
    suspended: bool
    linked_at: str

    @classmethod
    def _from(cls, d: Dict[str, Any]) -> "Installation":
        return cls(
            id=int(d["id"]),
            account_login=d.get("accountLogin", ""),
            account_type=d.get("accountType", ""),
            suspended=bool(d.get("suspended", False)),
            linked_at=d.get("linkedAt", ""),
        )


@dataclass
class InstallationList:
    eligible: bool
    reason: str
    allowed_sizes: List[str] = field(default_factory=list)
    installations: List[Installation] = field(default_factory=list)


@dataclass
class RunnerJob:
    id: str
    repo: str
    workflow: str
    job_name: str
    label: str
    size: str
    status: str
    reason: str
    sandbox_id: Optional[str]
    queued_at: Optional[str]
    started_at: Optional[str]
    finished_at: Optional[str]
    conclusion: Optional[str]
    github_url: str

    @classmethod
    def _from(cls, d: Dict[str, Any]) -> "RunnerJob":
        return cls(
            id=d["id"],
            repo=d.get("repo", ""),
            workflow=d.get("workflow", ""),
            job_name=d.get("jobName", ""),
            label=d.get("label", ""),
            size=d.get("size", ""),
            status=d.get("status", ""),
            reason=d.get("reason", ""),
            sandbox_id=d.get("sandboxId") or None,
            queued_at=d.get("queuedAt") or None,
            started_at=d.get("startedAt") or None,
            finished_at=d.get("finishedAt") or None,
            conclusion=d.get("conclusion") or None,
            github_url=d.get("githubUrl", ""),
        )


class Runners:
    """``anona_compute.Runners()``: installations and jobs of Anona Runners."""

    def __init__(self, api_key: Optional[str] = None, api_url: Optional[str] = None):
        self._api_key = api_key
        self._api_url = api_url

    @property
    def _config(self) -> ConnectionConfig:
        apply_env()
        return ConnectionConfig(api_key=self._api_key, api_url=self._api_url)

    def list_installations(self) -> InstallationList:
        body = get_json(self._config, "/v1/runners/installations") or {}
        return InstallationList(
            eligible=bool(body.get("eligible", False)),
            reason=body.get("reason", ""),
            allowed_sizes=list(body.get("allowedSizes") or []),
            installations=[Installation._from(i) for i in body.get("installations") or []],
        )

    def link_url(self) -> str:
        body = post_json(self._config, "/v1/runners/installations/link") or {}
        return body.get("url", "")

    def list_jobs(
        self, repo: Optional[str] = None, status: Optional[str] = None, limit: int = 20
    ) -> List[RunnerJob]:
        params = {}
        if repo:
            params["repo"] = repo
        if status:
            params["status"] = status
        params["limit"] = str(limit)
        body = get_json(self._config, "/v1/runners/jobs?" + urlencode(params, quote_via=quote)) or {}
        return [RunnerJob._from(j) for j in body.get("jobs") or []]

    def get_job(self, id: str) -> RunnerJob:
        body = get_json(self._config, f"/v1/runners/jobs/{quote(id, safe='')}") or {}
        return RunnerJob._from(body)
