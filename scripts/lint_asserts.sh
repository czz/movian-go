#!/usr/bin/env bash
# cleanup lint gate (local): blocks fragile type assertions on callback args —
# the bug class behind Buksa #13 (ExtArg sweep). Must stay at ZERO new sites.
#
# Exempt: event.go internals (ExtArg/AsEvent implementations),
#         test files, comments mentioning the pattern.
set -u
fail=0

# 1. .(*event.Event) assertions outside event package internals
hits=$(grep -rn '\.(\*event\.Event)\|\.(\*Event)' --include='*.go' internal/ cmd/ \
  | grep -v '_test.go' | grep -v 'internal/event/event.go' | grep -v 'ExtArg\|AsEvent\|// args\[0\]' || true)
if [ -n "$hits" ]; then echo "FAIL: (*event.Event) assertions:"; echo "$hits"; fail=1; fi

# 2. silent prop-arg assertions (args[i].(*Prop) with ignored ok or hard assert)
hits=$(grep -rn 'args\[[0-9]\]\.(\*propcore\.Prop)\|args\[[0-9]\]\.(\*prop\.Prop)\|args\[[0-9]\]\.(\*Prop)' --include='*.go' internal/ \
  | grep -v '_test.go' | grep -v 'ArgProp' || true)
if [ -n "$hits" ]; then echo "WARN: prop-arg assertions (audit each):"; echo "$hits" | head -20; fi

# 3. read-only lookups that mutate: pm.Subfind outside prop package
hits=$(grep -rn '\.Subfind(' --include='*.go' internal/ \
  | grep -v '_test.go' | grep -v 'internal/prop/' || true)
if [ -n "$hits" ]; then echo "INFO: Subfind callsites (must use typed Resolve/Ensure after F1):"; echo "$hits"; fi

[ "$fail" = 0 ] && echo "cleanup-lint: OK"
exit $fail
