"""Deterministic gate: the greeting must be "hello factory".

This test is the fixture's EXPECTED POST-CHANGE state. Before the agent runs it
fails, which is what makes it a real gate rather than a formality.
"""

from greeting import greeting


def test_greeting_is_updated() -> None:
    actual = greeting()
    assert actual == "hello factory", f"expected 'hello factory', got {actual!r}"


if __name__ == "__main__":
    test_greeting_is_updated()
    print("tests passed")
