#!/usr/bin/env python3
"""Refresh WorkBuddy's catalog and CPA registrations without restarting CPA."""
import argparse
import datetime as dt
import json
import os
from pathlib import Path
import sys
import time
import urllib.error
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", default="http://127.0.0.1:8317")
    parser.add_argument("--credentials-file")
    args = parser.parse_args()
    path = Path(args.credentials_file) if args.credentials_file else (
        Path(os.environ.get("CREDENTIALS_DIRECTORY", "/run/credentials/codebuddy-model-refresh.service")) / "management.json"
    )
    credentials = json.loads(path.read_text(encoding="utf-8"))
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    base = args.base_url.rstrip("/")

    def call(route, key="management_key", data=None):
        headers = {"Authorization": "Bearer " + credentials[key]}
        payload = None
        if data is not None:
            headers["Content-Type"] = "application/json"
            payload = json.dumps(data).encode()
        request = urllib.request.Request(base + route, data=payload, headers=headers,
                                         method="PATCH" if data is not None else "GET")
        with opener.open(request, timeout=15) as response:
            return json.load(response)

    plugins = call("/v0/management/plugins")
    plugin = next((p for p in plugins.get("plugins", []) if p.get("id") == "workbuddy"), None)
    if not plugin or not plugin.get("effective_enabled"):
        print(json.dumps({"status": "skipped", "reason": "plugin_disabled"}))
        return 0
    if plugin.get("metadata", {}).get("version") != "0.2.0-catalog":
        raise RuntimeError("dynamic_catalog_plugin_not_active")
    before = call("/v1/models", "api_key")
    previous = {m.get("id"): m.get("catalog", {}).get("fetched_at")
                for m in before.get("data", []) if m.get("owned_by") == "workbuddy"}
    nonce = time.time_ns()
    call("/v0/management/plugins/workbuddy/config", data={"_catalog_refresh": nonce})
    deadline = time.monotonic() + 45
    while time.monotonic() < deadline:
        models = call("/v1/models", "api_key")
        rows = [m for m in models.get("data", []) if m.get("owned_by") == "workbuddy"]
        if rows and all(m.get("catalog", {}).get("fetched_at")
                        and m["catalog"]["fetched_at"] != previous.get(m.get("id"))
                        and not m["catalog"].get("stale") for m in rows):
            result = {"status": "refreshed", "model_count": len(rows),
                      "channels": sorted({m["catalog"]["channel"] for m in rows}),
                      "time_utc": dt.datetime.now(dt.timezone.utc).isoformat(),
                      "model_ids": [m["id"] for m in rows]}
            print(json.dumps(result, ensure_ascii=False))
            return 0
        time.sleep(1)
    raise RuntimeError("catalog_refresh_not_confirmed")


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except urllib.error.HTTPError as error:
        print(json.dumps({"status": "failed", "reason": "management_http_error", "http_status": error.code}))
        raise SystemExit(1)
    except Exception as error:
        # Never print request objects, authorization headers or credential values.
        reason = str(error) if isinstance(error, RuntimeError) else type(error).__name__
        print(json.dumps({"status": "failed", "reason": reason}))
        raise SystemExit(1)
