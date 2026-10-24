#!/usr/bin/env sh
# Validates the alert rules (syntax + unit tests) and the dashboard JSON. Uses a local promtool or the pinned image.
set -eu
cd "$(dirname "$0")"
PROM_IMAGE=prom/prometheus:v3.5.0@sha256:63805ebb8d2b3920190daf1cb14a60871b16fd38bed42b857a3182bc621f4996
if command -v promtool >/dev/null 2>&1; then
  promtool check rules deed-alerts.yaml && promtool test rules deed-alerts.test.yaml
else
  docker run --rm -v "$PWD:/w:ro" -w /w --entrypoint promtool "$PROM_IMAGE" check rules deed-alerts.yaml
  docker run --rm -v "$PWD:/w:ro" -w /w --entrypoint promtool "$PROM_IMAGE" test rules deed-alerts.test.yaml
fi
python3 -c 'import json,sys; d=json.load(open("deed-dashboard.json")); assert d["uid"]=="deed-overview"; print("dashboard ok:", len(d["panels"]), "panels")'
