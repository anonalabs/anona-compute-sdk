"""Anona Compute extras on top of the E2B SDK.

The stock E2B SDK works against Anona with ``E2B_DOMAIN=compute.anonalabs.com``
and ``E2B_API_KEY``; this package is optional. It adds:

- ``anona_compute.configure(api_key=...)`` and ``anona_compute.Sandbox``, a subclass of
  ``e2b.Sandbox`` that defaults to the Anona domain.
- ``sandbox.live``: viewer tokens and embed URLs for the live view.
- ``sandbox.ssh``: short-lived SSH certificates and an ``ssh_config`` snippet.
- ``sandbox.idle_pause``: pause the sandbox once nothing has used it for a while.
- ``Sandbox.create`` and ``Sandbox.connect`` wait for capacity: while the fleet
  is full they retry a ``503`` marked ``Should-Retry: true`` after its
  ``Retry-After``, for up to 10 minutes (``wait_for_capacity``, ``0`` = off).

Environment mapping (only variables the E2B SDK 2.61.0 reads in
``e2b/connection_config.py`` are targeted):

==================  ==============  ==========================================
Anona variable      E2B variable    Notes
==================  ==============  ==========================================
``ANONA_API_KEY``   ``E2B_API_KEY`` used when ``E2B_API_KEY`` is unset
``ANONA_DOMAIN``    ``E2B_DOMAIN``  default ``compute.anonalabs.com``
``ANONA_API_URL``   ``E2B_API_URL`` deprecated by E2B; fallback only
==================  ==============  ==========================================

``E2B_*`` values always win over ``ANONA_*`` values.
"""

from importlib.metadata import PackageNotFoundError, version as _version

from anona_compute._capacity import DEFAULT_WAIT_FOR_CAPACITY, CapacityWait
from anona_compute._config import DEFAULT_API_URL, DEFAULT_DOMAIN, ENV_MAP, configure
from anona_compute.idle import MAX_IDLE_PAUSE_MINUTES, IdlePause
from anona_compute.live import Live, ViewerToken
from anona_compute.sandbox import SIZES, Sandbox, SandboxSize, size_metadata
from anona_compute.ssh import Ssh, SshCertificate

try:
    __version__ = _version("anona-compute")
except PackageNotFoundError:  # running from a source checkout
    __version__ = "0.0.0"

__all__ = [
    "CapacityWait",
    "DEFAULT_API_URL",
    "DEFAULT_DOMAIN",
    "DEFAULT_WAIT_FOR_CAPACITY",
    "ENV_MAP",
    "MAX_IDLE_PAUSE_MINUTES",
    "IdlePause",
    "Live",
    "SIZES",
    "Sandbox",
    "SandboxSize",
    "Ssh",
    "SshCertificate",
    "ViewerToken",
    "size_metadata",
    "__version__",
    "configure",
]
