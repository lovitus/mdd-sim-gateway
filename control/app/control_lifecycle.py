"""Process-local lifecycle fence shared by the Control runner and application."""

import threading


_shutdown_started = threading.Event()


def begin_shutdown() -> None:
    """Fence hardware-loss side effects before network listeners start closing."""
    _shutdown_started.set()


def reset_for_startup() -> None:
    """Open the fence once, before a new Control process starts serving."""
    _shutdown_started.clear()


def shutdown_started() -> bool:
    return _shutdown_started.is_set()
