"""``CapacityWaitMixin``: the capacity wait on ``create`` and ``connect``.

It sits directly in front of ``e2b.Sandbox`` in the class order, so for
``anona_compute.desktop.Sandbox`` it wraps only the API call underneath
e2b-desktop's own ``create`` (the desktop start-up after it is never repeated).
"""

from __future__ import annotations

from typing import Any, Optional

import e2b
from e2b.sandbox.utils import class_method_variant

from anona_compute import _capacity
from anona_compute._capacity import OnCapacityWait

_capacity.install()


class CapacityWaitMixin(e2b.Sandbox):
    @classmethod
    def create(  # type: ignore[override]
        cls,
        *args: Any,
        wait_for_capacity: Optional[float] = None,
        on_capacity_wait: Optional[OnCapacityWait] = None,
        **kwargs: Any,
    ):
        parent = super()
        return _capacity.call(
            lambda: parent.create(*args, **kwargs),
            what="sandbox create",
            wait=wait_for_capacity,
            on_wait=on_capacity_wait,
        )

    @classmethod
    def _cls_connect_sandbox(  # type: ignore[override]
        cls,
        *args: Any,
        wait_for_capacity: Optional[float] = None,
        on_capacity_wait: Optional[OnCapacityWait] = None,
        **kwargs: Any,
    ):
        parent = super()
        return _capacity.call(
            lambda: parent._cls_connect_sandbox(*args, **kwargs),
            what="sandbox connect",
            wait=wait_for_capacity,
            on_wait=on_capacity_wait,
        )

    @class_method_variant("_cls_connect_sandbox")
    def connect(  # type: ignore[override]
        self,
        *args: Any,
        wait_for_capacity: Optional[float] = None,
        on_capacity_wait: Optional[OnCapacityWait] = None,
        **kwargs: Any,
    ):
        """``e2b.Sandbox.connect`` (which resumes a paused sandbox) plus the
        capacity wait: ``wait_for_capacity`` seconds (``0`` turns it off) and
        ``on_capacity_wait``, called before each wait."""
        parent = super()
        return _capacity.call(
            lambda: parent.connect(*args, **kwargs),
            what="sandbox connect",
            wait=wait_for_capacity,
            on_wait=on_capacity_wait,
        )
