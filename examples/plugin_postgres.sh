#!/bin/bash
# Example PulseWatch plugin: postgres metrics
# Usage: ./postgres_plugin.sh
# Output: JSON object with metric name → float64

PGPASSWORD="${PG_PASS:-}" psql -h "${PG_HOST:-localhost}" -U "${PG_USER:-postgres}" \
  -t -A -F'=' -c "
SELECT
  'connections_total='  || count(*)                                    FROM pg_stat_activity;
" 2>/dev/null | python3 -c "
import sys
lines = [l.strip() for l in sys.stdin if '=' in l]
d = {}
for l in lines:
    k, v = l.split('=', 1)
    try: d[k.strip()] = float(v.strip())
    except: pass
import json; print(json.dumps(d))
"
