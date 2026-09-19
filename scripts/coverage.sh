#!/usr/bin/env bash
# Every package over a floor, from a coverage profile.
#
set -euo pipefail

profile=${1:-coverage.out}
min=${2:-90}
skip='__none__'

[ -f "$profile" ] || { echo "no $profile — run the tests with -coverprofile first"; exit 1; }

# The sort key in column one keeps the package rows together and ahead of the
# summary, whatever order awk walked the map in. It is stripped on the way out.
awk -v min="$min" -v skip="$skip" '
  NR == 1 && /^mode:/ { next }
  {
    split($1, a, ":")
    n = split(a[1], p, "/")
    dir = p[1]
    for (i = 2; i < n; i++) dir = dir "/" p[i]
    stmts[dir] += $2
    if ($3 > 0) covered[dir] += $2
  }
  END {
    bad = 0
    for (d in stmts) {
      if (d ~ skip) { skipped = skipped (skipped ? ", " : "") d; continue }
      pct = stmts[d] ? 100 * covered[d] / stmts[d] : 0
      total += stmts[d]; hit += covered[d]
      status = (pct + 1e-9 < min) ? "FAIL" : "ok"
      if (status == "FAIL") bad = 1
      printf "1 %-5s %6.1f%%  %s  (%d/%d)\n", status, pct, d, covered[d], stmts[d]
    }
    printf "2 \n"
    printf "2 %-5s %6.1f%%  TOTAL  (%d/%d)\n", (bad ? "FAIL" : "ok"), total ? 100 * hit / total : 0, hit, total
    if (skipped) printf "3 \n3 excluded: %s\n", skipped
    if (bad) printf "3 \n3 every package has to hold %g%%\n", min
    exit bad
  }
' "$profile" | sort -k1,1 -k4,4 | cut -d" " -f2-
