#!/usr/bin/env bash
# DeepSentry v2.0.4 CI: reachable vulnerability gate for release binaries.

set -euo pipefail

binary=${1:?usage: verify-vulnerabilities.sh /path/to/deepsentry}
scanner=${GOVULNCHECK:-govulncheck}

# GO-2026-5932 marks every symbol in x/crypto/openpgp as vulnerable. Binary
# mode cannot distinguish that wildcard advisory from an unrelated package in
# the same x/crypto module, so it reports OpenPGP whenever module metadata is
# present. Fail independently if any OpenPGP package ever becomes a real
# dependency; only that documented binary-only false positive is filtered.
if go list -deps ./cmd | grep -Eq '^golang.org/x/crypto/openpgp(/|$)'; then
  echo "forbidden dependency: golang.org/x/crypto/openpgp is unsafe and unmaintained" >&2
  exit 1
fi

report=$(mktemp "${TMPDIR:-/tmp}/deepsentry-vuln.XXXXXX")
trap 'trash "$report" 2>/dev/null || unlink "$report" 2>/dev/null || true' EXIT

set +e
"$scanner" -json -mode=binary -scan=symbol "$binary" >"$report"
status=$?
set -e
if [[ $status -ne 0 && $status -ne 3 ]]; then
  echo "govulncheck failed with status $status" >&2
  exit "$status"
fi

python3 - "$report" <<'PY'
import json
import sys

raw = open(sys.argv[1], "r", encoding="utf-8").read()
decoder = json.JSONDecoder()
objects = []
offset = 0
while offset < len(raw):
    while offset < len(raw) and raw[offset].isspace():
        offset += 1
    if offset >= len(raw):
        break
    value, offset = decoder.raw_decode(raw, offset)
    objects.append(value)

if not objects or not any("config" in value for value in objects):
    raise SystemExit("govulncheck produced no valid scan metadata")
errors = [value["error"] for value in objects if "error" in value]
if errors:
    raise SystemExit(f"govulncheck reported scanner errors: {errors}")

found = {value["finding"]["osv"] for value in objects if "finding" in value}
allowed = {"GO-2026-5932"}
blocking = sorted(found - allowed)
if blocking:
    raise SystemExit("reachable vulnerabilities: " + ", ".join(blocking))

if found:
    print("No reachable vulnerabilities; ignored GO-2026-5932 binary wildcard after import-graph check.")
else:
    print("No reachable vulnerabilities.")
PY
