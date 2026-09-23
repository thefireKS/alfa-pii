#!/usr/bin/env python3
"""Replay-boundary and lost-first-restore test for pii-service.

Verifies the two-phase retention policy end-to-end through the public POST
/process API:

  1. A new pair masks and stores the correspondence (pending phase).
  2. The first restore returns the exact original and moves the record to the
     replay phase (so a lost response can be repeated).
  3. A repeat of the mask within the replay window returns the original again
     (simulating a lost first response).
  4. A repeat at the replay_ttl boundary still returns the original.
  5. After the replay_ttl elapses, the record is gone: a repeat of the mask is
     treated as a new text and returns it unchanged (no state), and a repeat of
     the original creates a new correspondence.

Only synthetic data is used. Uses only the Python 3 standard library.

Usage:
    python3 replay-boundary-test.py [base_url] [--replay-ttl SECONDS]
                                    [--margin SECONDS] [--out DIR]
"""

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request

DEFAULT_BASE = "http://127.0.0.1:8080"


def post(base, payload_id, payload, timeout=10.0):
    body = json.dumps({"payload": payload, "payload_id": payload_id}).encode("utf-8")
    req = urllib.request.Request(
        base + "/process",
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            data = json.loads(resp.read().decode("utf-8"))
            return resp.status, data.get("result")
    except urllib.error.HTTPError as e:
        try:
            data = json.loads(e.read().decode("utf-8"))
            msg = data.get("error", "")
        except Exception:
            msg = ""
        return e.code, msg
    except Exception as e:
        return 0, str(e)


def main():
    ap = argparse.ArgumentParser(description="Replay-boundary test for pii-service")
    ap.add_argument("base", nargs="?", default=DEFAULT_BASE)
    ap.add_argument("--replay-ttl", type=int, default=120, help="replay TTL in seconds")
    ap.add_argument("--margin", type=int, default=5, help="margin before the boundary")
    ap.add_argument("--out", default=".artifacts", help="output directory")
    args = ap.parse_args()

    base = args.base.rstrip("/")
    out = args.out

    def w(msg):
        print(msg, flush=True)

    w("=== Replay-boundary / lost-first-restore test ===")
    w("Base: %s, replay TTL: %ds, margin: %ds" % (base, args.replay_ttl, args.margin))

    # Unique suffix so a repeated run against a live service does not collide
    # with a correspondence created by an earlier run.
    suffix = str(int(time.time() * 1000))
    pid = "replay-boundary-1-" + suffix
    pid2 = "replay-boundary-2-" + suffix
    original = "Email: boundary@example.test"

    # 1. Mask a new pair.
    status, mask = post(base, pid, original)
    w("1. mask new pair: status=%d mask=%s" % (status, mask))
    if status != 200 or mask == original:
        w("FAIL: expected a mask, got status=%d" % status)
        sys.exit(1)

    # 2. First restore -> exact original, moves to replay phase.
    status, result = post(base, pid, mask)
    w("2. first restore: status=%d exact=%s" % (status, result == original))
    if status != 200 or result != original:
        w("FAIL: first restore did not return the original")
        sys.exit(1)

    # 3. Repeat the mask within the replay window (simulate lost first response).
    status, result = post(base, pid, mask)
    w("3. repeat mask in replay window: status=%d exact=%s" % (status, result == original))
    if status != 200 or result != original:
        w("FAIL: repeat within replay window did not return the original")
        sys.exit(1)

    # 4. Wait until just before the replay_ttl boundary and repeat again.
    wait = max(0, args.replay_ttl - args.margin)
    w("4. waiting %d s to reach the replay boundary..." % wait)
    time.sleep(wait)
    status, result = post(base, pid, mask)
    w("   repeat at replay boundary: status=%d exact=%s" % (status, result == original))
    if status != 200 or result != original:
        w("FAIL: repeat at replay boundary did not return the original")
        sys.exit(1)

    # 5. Wait past the replay_ttl boundary; the record should be gone.
    wait = args.margin + 5
    w("5. waiting %d s past the replay boundary..." % wait)
    time.sleep(wait)
    status, result = post(base, pid, mask)
    w("   repeat after replay expiry: status=%d result=%s" % (status, result))
    # After expiry the mask is treated as a new text and returned unchanged.
    if status != 200 or result != mask:
        w("FAIL: expected the expired mask to be treated as a new text (returned unchanged)")
        sys.exit(1)

    # 6. A fresh payload_id after expiry creates a new correspondence. A
    # separate ID is used because step 5 re-created the record for pid with the
    # mask as its original, so reusing pid would conflict.
    status, mask2 = post(base, pid2, original)
    w("6. new id after expiry: status=%d mask=%s" % (status, mask2))
    if status != 200 or mask2 == original:
        w("FAIL: expected a new mask for a fresh id after expiry")
        sys.exit(1)

    w("=== Result: PASS ===")
    summary = {
        "base": base,
        "replay_ttl": args.replay_ttl,
        "mask_status": 200,
        "first_restore_exact": True,
        "repeat_in_window_exact": True,
        "repeat_at_boundary_exact": True,
        "repeat_after_expiry_unchanged": True,
        "new_mask_after_expiry": True,
        "result": "PASS",
    }
    os.makedirs(out, exist_ok=True)
    with open(os.path.join(out, "replay-boundary.json"), "w") as f:
        json.dump(summary, f, indent=2)
    w("Report written to %s/replay-boundary.json" % out)


if __name__ == "__main__":
    main()