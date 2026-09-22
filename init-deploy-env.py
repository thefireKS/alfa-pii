#!/usr/bin/env python3
"""Create .env.deploy in the current project directory without replacing it."""
import os
import secrets
import sys


def main():
    try:
        fd = os.open(".env.deploy", os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        print(".env.deploy already exists; left unchanged.")
        return 0
    except OSError as exc:
        print(f"FAIL: cannot create .env.deploy ({type(exc).__name__})", file=sys.stderr)
        return 1

    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as stream:
            stream.write("PII_CONSUMER_DEMO_SECRET=" + secrets.token_hex(32) + "\n")
    except OSError as exc:
        print(f"FAIL: cannot write .env.deploy ({type(exc).__name__})", file=sys.stderr)
        return 1

    print(".env.deploy created; secret was not printed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
