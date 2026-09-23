#!/usr/bin/env python3
"""Store-fill test for pii-service.

Fills the in-memory store to its capacity limit through the public POST
/process API with known synthetic pairs, then verifies:

  1. At capacity, a concurrent load at ~330 RPS with peaks to 1000 gives new
     requests fast controlled 429 refusals.
  2. Existing pairs restore exactly and repeat (mask -> original, repeat
     original -> same mask) under the same load.
  3. After the pending TTL and replay TTL elapse, new writes are accepted
     again without a service restart.

Only synthetic data is used. No real personal data, keys or secrets are
printed or logged. The script uses only the Python 3 standard library.

Usage:
    python3 store-fill-test.py [base_url] [--fill N] [--ttl SECONDS]
                               [--replay-ttl SECONDS] [--load SECONDS]
                               [--target RPS] [--peak RPS] [--out DIR]
"""

import argparse
import json
import os
import sys
import threading
import time
import urllib.error
import urllib.request
from collections import Counter

DEFAULT_BASE = "http://127.0.0.1:8080"


def post(base, payload_id, payload, timeout=10.0):
    """Send one POST /process request. Returns (status, result_or_error, latency)."""
    body = json.dumps({"payload": payload, "payload_id": payload_id}).encode("utf-8")
    req = urllib.request.Request(
        base + "/process",
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    start = time.monotonic()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            data = json.loads(resp.read().decode("utf-8"))
            return resp.status, data.get("result"), time.monotonic() - start
    except urllib.error.HTTPError as e:
        try:
            data = json.loads(e.read().decode("utf-8"))
            msg = data.get("error", "")
        except Exception:
            msg = ""
        return e.code, msg, time.monotonic() - start
    except Exception as e:
        return 0, str(e), time.monotonic() - start


def synthetic_text(i):
    """Build a deterministic synthetic text with one email entity."""
    return "Email: user%d@example.test" % i


def percentile(sorted_vals, p):
    if not sorted_vals:
        return 0.0
    idx = min(len(sorted_vals) - 1, int(len(sorted_vals) * p))
    return sorted_vals[idx]


def main():
    ap = argparse.ArgumentParser(description="Store-fill test for pii-service")
    ap.add_argument("base", nargs="?", default=DEFAULT_BASE)
    ap.add_argument("--fill", type=int, default=120000, help="max pre-fill pairs")
    ap.add_argument("--ttl", type=int, default=600, help="pending TTL in seconds")
    ap.add_argument("--replay-ttl", type=int, default=120, help="replay TTL in seconds")
    ap.add_argument("--load", type=int, default=60, help="load phase duration in seconds")
    ap.add_argument("--target", type=float, default=330, help="target RPS in load phase")
    ap.add_argument("--peak", type=float, default=1000, help="peak RPS in load phase")
    ap.add_argument("--out", default=".artifacts", help="output directory")
    args = ap.parse_args()

    base = args.base.rstrip("/")
    out = args.out

    def w(msg):
        print(msg, flush=True)

    w("=== Store-fill test ===")
    w("Base: %s" % base)
    w("Pending TTL: %ds, replay TTL: %ds" % (args.ttl, args.replay_ttl))

    # ---- Phase 1: pre-fill the store to capacity ----
    w("Phase 1: pre-filling store with known synthetic pairs...")
    pairs = []  # (payload_id, original, mask)
    first_429 = None
    fill_start = time.monotonic()
    i = 0
    while i < args.fill:
        pid = "fill-%d" % i
        original = synthetic_text(i)
        status, result, lat = post(base, pid, original)
        if status == 200:
            pairs.append((pid, original, result))
            i += 1
            if i % 20000 == 0:
                w("  filled %d pairs (last latency %.1f ms)" % (i, lat * 1000))
        elif status == 429:
            first_429 = (pid, lat)
            w("  store full at %d pairs; first 429 on new id %s (latency %.1f ms)"
              % (i, pid, lat * 1000))
            break
        else:
            w("  unexpected status %d on fill id %s: %s" % (status, pid, result))
            sys.exit(1)
    fill_dur = time.monotonic() - fill_start
    w("Pre-fill: %d pairs in %.1f s (%.0f pairs/s)" % (len(pairs), fill_dur, len(pairs) / fill_dur))
    if first_429 is None:
        w("WARNING: store did not reach capacity within --fill=%d; results may be partial" % args.fill)

    # ---- Phase 2: concurrent load at capacity ----
    w("Phase 2: concurrent load at ~%.0f RPS with peaks to %.0f for %d s..."
      % (args.target, args.peak, args.load))

    # Build a pool of existing pairs to restore. Use a spread across the whole
    # pre-filled set so restores exercise records of different ages.
    pool = pairs[:: max(1, len(pairs) // 2000)]
    if not pool:
        pool = pairs
    pool_lock = threading.Lock()
    pool_idx = [0]

    # Shared counters.
    counters = {
        "new_429": 0, "new_other": 0, "new_429_lat": [],
        "restore_ok": 0, "restore_fail": 0, "restore_lat": [],
        "repeat_ok": 0, "repeat_fail": 0,
        "timeout": 0, "error": 0,
    }
    counters_lock = threading.Lock()
    # iter_idx is incremented on every request so new/restore alternate across
    # all workers regardless of which branch runs.
    iter_idx = [0]

    def next_pool_pair():
        with pool_lock:
            p = pool[pool_idx[0] % len(pool)]
            pool_idx[0] += 1
            return p

    def worker(stop_event, next_slot):
        while not stop_event.is_set():
            # Pace to the target rate: wait until the next slot time.
            t = next_slot()
            if t is None:
                return
            now = time.monotonic()
            if now < t:
                time.sleep(t - now)
            with counters_lock:
                idx = iter_idx[0]
                iter_idx[0] += 1
            # Alternate new requests and restores roughly 50/50.
            if idx % 2 == 0:
                # New request: expect 429 at capacity.
                pid = "load-new-%d" % idx
                status, result, lat = post(base, pid, synthetic_text(3000000 + idx))
                with counters_lock:
                    if status == 429:
                        counters["new_429"] += 1
                        counters["new_429_lat"].append(lat)
                    elif status == 0:
                        counters["timeout"] += 1
                    else:
                        counters["new_other"] += 1
            else:
                # Restore an existing pair, then repeat the original.
                pid, original, mask = next_pool_pair()
                status, result, lat = post(base, pid, mask)
                with counters_lock:
                    if status == 200 and result == original:
                        counters["restore_ok"] += 1
                        counters["restore_lat"].append(lat)
                    elif status == 0:
                        counters["timeout"] += 1
                    else:
                        counters["restore_fail"] += 1
                status, result, lat = post(base, pid, original)
                with counters_lock:
                    if status == 200 and result == mask:
                        counters["repeat_ok"] += 1
                    elif status == 0:
                        counters["timeout"] += 1
                    else:
                        counters["repeat_fail"] += 1

    # Rate limiter: schedule slots at the target rate, with periodic bursts to
    # the peak rate. Slots are generated on a monotonic timeline.
    load_start = time.monotonic()
    stop_event = threading.Event()
    slot_lock = threading.Lock()
    next_slot_time = [load_start]
    burst_until = [0.0]

    def next_slot():
        with slot_lock:
            now = time.monotonic()
            # Burst windows: for 5s every 30s, run at the peak rate.
            if now >= burst_until[0]:
                burst_until[0] = now + 30.0
            in_burst = now < burst_until[0] and (now - burst_until[0] + 30.0) < 5.0
            rate = args.peak if in_burst else args.target
            t = next_slot_time[0]
            next_slot_time[0] = max(t, now) + 1.0 / rate
            return t

    n_workers = 200
    threads = []
    for _ in range(n_workers):
        th = threading.Thread(target=worker, args=(stop_event, next_slot), daemon=True)
        th.start()
        threads.append(th)

    time.sleep(args.load)
    stop_event.set()
    for th in threads:
        th.join(timeout=5)
    load_dur = time.monotonic() - load_start

    with counters_lock:
        c = dict(counters)
    total = c["new_429"] + c["new_other"] + c["restore_ok"] + c["restore_fail"] + c["timeout"]
    w("  load phase: %.1f s, total requests=%d (%.0f RPS)" % (load_dur, total, total / load_dur))
    w("  new requests: 429=%d other=%d" % (c["new_429"], c["new_other"]))
    if c["new_429_lat"]:
        l = sorted(c["new_429_lat"])
        w("  429 latency: mean=%.1f ms p50=%.1f ms p95=%.1f ms p99=%.1f ms"
          % (sum(l) / len(l) * 1000, percentile(l, 0.5) * 1000,
             percentile(l, 0.95) * 1000, percentile(l, 0.99) * 1000))
    w("  existing pairs: restore_ok=%d restore_fail=%d repeat_ok=%d repeat_fail=%d"
      % (c["restore_ok"], c["restore_fail"], c["repeat_ok"], c["repeat_fail"]))
    if c["restore_lat"]:
        l = sorted(c["restore_lat"])
        w("  restore latency: mean=%.1f ms p50=%.1f ms p95=%.1f ms p99=%.1f ms"
          % (sum(l) / len(l) * 1000, percentile(l, 0.5) * 1000,
             percentile(l, 0.95) * 1000, percentile(l, 0.99) * 1000))
    w("  timeouts=%d" % c["timeout"])

    # ---- Phase 3: recovery after TTL expiry ----
    wait = args.ttl + 30
    w("Phase 3: waiting %d s for pending TTL expiry and replay freeing..." % wait)
    time.sleep(wait)

    accepted = 0
    refused = 0
    for k in range(50):
        pid = "recover-%d" % k
        status, result, lat = post(base, pid, synthetic_text(2000000 + k))
        if status == 200:
            accepted += 1
        elif status == 429:
            refused += 1
        else:
            w("  recovery unexpected status %d" % status)
    w("  recovery: new writes accepted=%d refused=%d" % (accepted, refused))

    ok = (c["new_429"] > 0 and c["restore_fail"] == 0 and c["repeat_fail"] == 0 and accepted > 0)
    w("=== Result: %s ===" % ("PASS" if ok else "FAIL"))
    summary = {
        "base": base,
        "prefill_pairs": len(pairs),
        "prefill_duration_s": round(fill_dur, 2),
        "first_429": first_429,
        "load_duration_s": round(load_dur, 2),
        "load_total": total,
        "load_rps": round(total / load_dur, 1),
        "new_429": c["new_429"],
        "new_other": c["new_other"],
        "restore_ok": c["restore_ok"],
        "restore_fail": c["restore_fail"],
        "repeat_ok": c["repeat_ok"],
        "repeat_fail": c["repeat_fail"],
        "timeouts": c["timeout"],
        "recovery_accepted": accepted,
        "recovery_refused": refused,
        "result": "PASS" if ok else "FAIL",
    }
    os.makedirs(out, exist_ok=True)
    with open(os.path.join(out, "store-fill.json"), "w") as f:
        json.dump(summary, f, indent=2)
    w("Report written to %s/store-fill.json" % out)
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()