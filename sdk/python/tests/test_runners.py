import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

import pytest
from e2b.exceptions import AuthenticationException, NotFoundException

import anona_compute
from anona_compute.runners import Installation, InstallationList, RunnerJob, Runners
from fake_gateway import API_KEY

JOB = {
    "id": "j1", "repo": "acme/app", "workflow": "ci", "jobName": "build", "label": "anona-small",
    "size": "small", "status": "running", "reason": "", "sandboxId": "sbx1",
    "queuedAt": "2026-11-21T10:00:00Z", "startedAt": "2026-11-21T10:00:05Z", "finishedAt": None,
    "conclusion": None, "githubUrl": "https://github.com/acme/app/actions/runs/1/job/2",
}


class Server:
    def __init__(self):
        self.requests = []
        srv = self

        class H(BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def _send(self, code, body):
                data = json.dumps(body).encode()
                self.send_response(code)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def do_GET(self):
                srv.requests.append((self.path, self.headers.get("X-API-Key")))
                if self.headers.get("X-API-Key") != API_KEY:
                    return self._send(401, {"message": "bad key"})
                path = self.path.split("?")[0]
                if path == "/v1/runners/installations":
                    return self._send(200, {
                        "eligible": True, "reason": "", "allowedSizes": ["small", "medium"],
                        "installations": [{"id": 12345, "accountLogin": "acme", "accountType": "Organization",
                                           "suspended": False, "linkedAt": "2026-11-21T10:00:00Z"}]})
                if path == "/v1/runners/jobs":
                    return self._send(200, {"jobs": [JOB]})
                if path == "/v1/runners/jobs/j1":
                    return self._send(200, JOB)
                return self._send(404, {"message": "job not found"})

            def do_POST(self):
                srv.requests.append((self.path, self.headers.get("X-API-Key")))
                if self.path == "/v1/runners/installations/link":
                    return self._send(403, {"code": 403, "error_code": "runners_not_eligible", "message": "runners need a paid plan"})
                return self._send(404, {"message": "nf"})

        self.httpd = ThreadingHTTPServer(("127.0.0.1", 0), H)
        self.url = f"http://127.0.0.1:{self.httpd.server_address[1]}"
        threading.Thread(target=self.httpd.serve_forever, daemon=True).start()


@pytest.fixture
def srv():
    s = Server()
    yield s
    s.httpd.shutdown()
    s.httpd.server_close()


@pytest.fixture
def runners(srv, monkeypatch):
    monkeypatch.setenv("ANONA_API_KEY", API_KEY)
    return Runners(api_url=srv.url)


def test_exported():
    assert anona_compute.Runners is Runners


def test_list_installations(runners):
    out = runners.list_installations()
    assert isinstance(out, InstallationList)
    assert out.eligible and out.allowed_sizes == ["small", "medium"]
    assert out.installations == [
        Installation(id=12345, account_login="acme", account_type="Organization", suspended=False,
                     linked_at="2026-11-21T10:00:00Z")
    ]


def test_list_jobs_query_and_mapping(runners, srv):
    jobs = runners.list_jobs(status="running", limit=5)
    assert srv.requests[-1][0] == "/v1/runners/jobs?status=running&limit=5"
    assert isinstance(jobs[0], RunnerJob)
    assert jobs[0].github_url.endswith("/job/2") and jobs[0].job_name == "build"
    assert jobs[0].sandbox_id == "sbx1" and jobs[0].finished_at is None
    runners.list_jobs(repo="acme/app")
    assert srv.requests[-1][0] == "/v1/runners/jobs?repo=acme%2Fapp&limit=20"


def test_get_job_and_not_found(runners):
    assert runners.get_job("j1").id == "j1"
    with pytest.raises(NotFoundException):
        runners.get_job("nope")


def test_401_is_auth_error(srv, monkeypatch):
    monkeypatch.setenv("ANONA_API_KEY", "e2b_" + "00" * 20)
    with pytest.raises(AuthenticationException):
        Runners(api_url=srv.url).list_jobs()


def test_link_url_not_eligible_surfaces_message(runners):
    with pytest.raises(AuthenticationException, match="runners need a paid plan"):
        runners.link_url()
