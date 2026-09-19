"""Tiny end-to-end fixture: a greeting that the factory's agent must change.

This repository is deliberately trivial and deliberately NOT a real project.
The acceptance test changes "hello" to "hello factory" and the factory must
independently prove the change by running the deterministic gates.

Python is chosen because the CubeSandbox base image already ships python3, so
the verification gates need no toolchain installation and stay fast and
reproducible.
"""


def greeting() -> str:
    """Return the current greeting."""
    return "hello"


if __name__ == "__main__":
    print(greeting())
