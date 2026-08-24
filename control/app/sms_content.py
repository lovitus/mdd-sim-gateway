"""Classification helpers for SMS bodies that are safe to show to a person.

Some transports expose SIM data-download / OTA payloads through the same callback as text
messages.  Once the TP-DCS metadata has been consumed by a modem or IMS stack, C0/C1 control
characters are the remaining reliable fail-closed signal: they are protocol bytes, not useful
message text.  Keep normal Unicode (including combining marks, emoji and ZWJ sequences) intact.
"""
from __future__ import annotations

import unicodedata


_DISPLAYABLE_CONTROLS = {"\t", "\n", "\r", "\f"}


def is_displayable_sms_text(value: object) -> bool:
    """Return True only for a non-empty body suitable for message history and push UI."""
    text = str(value or "")
    if not text.strip():
        return False
    for char in text:
        if char in _DISPLAYABLE_CONTROLS:
            continue
        # Cc includes both C0 and C1 bytes; Cs catches invalid surrogate code points.
        if unicodedata.category(char) in {"Cc", "Cs"}:
            return False
    # A replacement character can occur in real user text, but several in one short body means
    # a byte payload was decoded with the wrong character encoding.  Do not push that as text.
    replacements = text.count("\ufffd")
    if replacements >= 2 and replacements * 10 >= len(text):
        return False
    return True
