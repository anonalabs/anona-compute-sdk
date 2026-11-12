import os
import shutil
import subprocess

import pytest
from e2b.exceptions import InvalidArgumentException

PUB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOrq9Z5n3jYbqK1x0aHk0yJ6l1m1nq3o9g2Yx5c7zQ0x me@host"


def test_certificate_from_key_string(sandbox, gateway):
    cert = sandbox.ssh.certificate(PUB)
    assert cert.cert.startswith("ssh-ed25519-cert-v01@openssh.com ")
    assert cert.user == "user"
    assert cert.host == sandbox.sandbox_id
    assert cert.proxy_command == f"anona-compute ssh --proxy {sandbox.sandbox_id}"
    assert cert.expires_at is not None
    _, path, _, body = gateway.requests[-1]
    assert path == f"/v1/sandboxes/{sandbox.sandbox_id}/ssh-certs"
    assert body == {"publicKey": PUB}
    assert cert.cert not in repr(cert)


@pytest.mark.skipif(shutil.which("ssh-keygen") is None, reason="ssh-keygen not installed")
def test_certificate_from_real_key_file(sandbox, gateway, tmp_path):
    key = tmp_path / "id_ed25519"
    subprocess.run(
        ["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", str(key)], check=True
    )
    cert = sandbox.ssh.certificate(str(key) + ".pub")
    assert gateway.requests[-1][3]["publicKey"] == (tmp_path / "id_ed25519.pub").read_text().strip()
    out = cert.write(str(key) + "-cert.pub")
    assert oct(os.stat(out).st_mode & 0o777) == "0o600"


@pytest.mark.skipif(shutil.which("ssh-keygen") is None, reason="ssh-keygen not installed")
@pytest.mark.parametrize("bits", ["384", "521"])
def test_ecdsa_384_and_521_keys_are_sent(sandbox, gateway, tmp_path, bits):
    # The gateway signs ECDSA P-384 and P-521 keys too (sshca.ParsePublicKey).
    key = tmp_path / "id_ecdsa"
    subprocess.run(
        ["ssh-keygen", "-q", "-t", "ecdsa", "-b", bits, "-N", "", "-f", str(key)], check=True
    )
    sandbox.ssh.certificate(str(key) + ".pub")
    assert gateway.requests[-1][3]["publicKey"].startswith(f"ecdsa-sha2-nistp{bits} ")


def test_private_key_and_garbage_are_rejected(sandbox, gateway, tmp_path):
    before = len(gateway.requests)
    # A certificate is not a public key, though its type starts like one.
    with pytest.raises(InvalidArgumentException):
        sandbox.ssh.certificate("ssh-ed25519-cert-v01@openssh.com AAAAIHNzaC1lZDI1NTE5LWNlcnQ me@host")
    with pytest.raises(InvalidArgumentException):
        sandbox.ssh.certificate("-----BEGIN OPENSSH PRIVATE KEY-----")  # gitleaks:allow (fake header: the SDK must refuse a private key)
    priv = tmp_path / "k"
    priv.write_text("-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n")  # gitleaks:allow (fake key file)
    with pytest.raises(InvalidArgumentException):
        sandbox.ssh.certificate(str(priv))
    assert len(gateway.requests) == before


def test_config_snippet(sandbox):
    text = sandbox.ssh.config(identity_file="~/.ssh/k")
    sid = sandbox.sandbox_id
    assert text.splitlines()[:6] == [
        f"Host anona-{sid}",
        f"  HostName {sid}",
        "  User user",
        "  ProxyCommand anona-compute ssh --proxy %h",
        "  IdentityFile ~/.ssh/k",
        "  CertificateFile ~/.ssh/k-cert.pub",
    ]


@pytest.mark.skipif(shutil.which("ssh") is None, reason="OpenSSH client not installed")
def test_config_snippet_parses_with_real_openssh(sandbox, tmp_path):
    cfg = tmp_path / "config"
    cfg.write_text(sandbox.ssh.config(identity_file=str(tmp_path / "k")))
    out = subprocess.run(
        ["ssh", "-G", "-F", str(cfg), f"anona-{sandbox.sandbox_id}"],
        check=True,
        capture_output=True,
        text=True,
    ).stdout
    assert f"hostname {sandbox.sandbox_id}" in out
    assert "user user" in out
    assert "proxycommand anona-compute ssh --proxy %h" in out
    assert f"certificatefile {tmp_path}/k-cert.pub" in out
