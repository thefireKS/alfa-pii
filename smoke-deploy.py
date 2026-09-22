#!/usr/bin/env python3
"""Check readiness and the public /process contract with synthetic data."""
import argparse
import json
import sys
import time
import uuid
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import Request, urlopen

MAX_RESPONSE_BYTES = 65536


def request(base, path, payload=None):
    data = None
    headers = {"Accept": "application/json"}
    if payload is not None:
        data = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        headers["Content-Type"] = "application/json"
    req = Request(base + path, data=data, headers=headers)
    try:
        with urlopen(req, timeout=10) as response:
            status = response.status
            body = response.read(MAX_RESPONSE_BYTES + 1)
            content_type = response.headers.get("Content-Type", "")
    except HTTPError as exc:
        status = exc.code
        exc.close()
        raise RuntimeError(f"{path}: HTTP {status}") from None
    except (URLError, OSError) as exc:
        raise RuntimeError(f"{path}: connection failed ({type(exc).__name__})") from None
    if status != 200:
        raise RuntimeError(f"{path}: expected HTTP 200, got {status}")
    if len(body) > MAX_RESPONSE_BYTES:
        raise RuntimeError(f"{path}: response exceeds smoke-test limit")
    if payload is None:
        return None
    if content_type.split(";", 1)[0].strip().lower() != "application/json":
        raise RuntimeError(f"{path}: expected application/json")
    try:
        parsed = json.loads(body)
    except (ValueError, UnicodeError):
        raise RuntimeError(f"{path}: invalid JSON") from None
    if not isinstance(parsed, dict) or not isinstance(parsed.get("result"), str):
        raise RuntimeError(f"{path}: result must be a string")
    return parsed["result"]


def run(base):
    for attempt in range(10):
        try:
            request(base, "/readyz")
            break
        except RuntimeError:
            if attempt == 9:
                raise
            time.sleep(0.5)
    request(base, "/livez")
    print("PASS: /readyz and /livez")

    payload_id = "deploy-" + uuid.uuid4().hex
    email = "alice@example.test"
    original = "Email: " + email
    original_request = {"payload": original, "payload_id": payload_id}
    masked = request(base, "/process", original_request)
    if masked == original or email.casefold() in masked.casefold():
        raise RuntimeError("/process: synthetic email was not masked")
    if not masked.startswith("Email: "):
        raise RuntimeError("/process: text outside the email was changed")
    print("PASS: email masked; surrounding text preserved")

    if request(base, "/process", original_request) != masked:
        raise RuntimeError("/process: repeated original returned a different mask")
    print("PASS: original retry")

    masked_request = {"payload": masked, "payload_id": payload_id}
    if request(base, "/process", masked_request) != original:
        raise RuntimeError("/process: original was not restored exactly")
    print("PASS: exact restoration")

    if request(base, "/process", masked_request) != original:
        raise RuntimeError("/process: restoration retry changed the result")
    print("PASS: restoration retry")
    print("PASS: deployment smoke test")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("base_url", nargs="?", default="http://127.0.0.1:8000")
    args = parser.parse_args()
    base = args.base_url.rstrip("/")
    parsed = urlsplit(base)
    if (parsed.scheme not in ("http", "https") or not parsed.hostname
            or parsed.username is not None or parsed.password is not None
            or parsed.path or parsed.query or parsed.fragment):
        parser.error("Use the base HTTP(S) address without credentials, path or query")
    try:
        run(base)
    except RuntimeError as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
