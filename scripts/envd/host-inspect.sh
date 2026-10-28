#!/bin/sh
# Deterministic, read-only host inspection for envd targets.
#
# Intended use: one remote shell call from an agent, e.g.
#   remote_<prefix>__shell {"command":"~/.local/bin/host-inspect.sh"}
# so the model reasons over fixed data instead of orchestrating dozens of
# round-trips. Deploy it once with the bridge's write tool (see
# docs/aicli/envd-selfhost-deploy.md §5.1).
set -u

echo "== host =="
hostname
uname -sr
date '+%F %T %Z'

echo "== uptime/load =="
uptime

echo "== cpu =="
nproc

echo "== memory (MiB) =="
free -m | sed -n '1,3p'

echo "== disk =="
df -h / 2>/dev/null
df -i / 2>/dev/null | tail -1

echo "== top dirs (root fs) =="
du -xh --max-depth=1 / 2>/dev/null | sort -h | tail -8

echo "== systemd =="
systemctl is-system-running 2>/dev/null || true
systemctl list-units --state=failed --no-pager 2>/dev/null | sed -n '1,12p'

echo "== listening (tcp) =="
ss -ltn 2>/dev/null | sed -n '1,20p'

echo "== http probes =="
for u in \
  http://127.0.0.1/ \
  http://127.0.0.1:8090/ \
  http://127.0.0.1:8090/actuator/health \
  http://127.0.0.1:8091/ \
  http://127.0.0.1:8091/actuator/health \
  http://127.0.0.1:3210/health
do
  code=$(curl -s -m 3 -o /dev/null -w '%{http_code}' "$u" 2>/dev/null || true)
  echo "$u -> ${code:-000}"
done

echo "== tcp probes =="
for p in 3306 6379 6380 5433; do
  if timeout 2 bash -c "</dev/tcp/127.0.0.1/$p" 2>/dev/null; then
    echo "tcp $p open"
  else
    echo "tcp $p closed"
  fi
done

echo "== top processes (mem) =="
ps -eo pid,pcpu,pmem,comm --sort=-pmem 2>/dev/null | head -10

echo "== syslog =="
if [ -r /var/log/syslog ]; then
  ls -l /var/log/syslog
  tail -n 3000 /var/log/syslog 2>/dev/null | grep -iE 'error|fail|crit|oom' | tail -10 || true
elif command -v sg >/dev/null 2>&1; then
  ls -l /var/log/syslog 2>/dev/null || true
  sg adm -c "tail -n 3000 /var/log/syslog 2>/dev/null | grep -iE 'error|fail|crit|oom' | tail -10" 2>/dev/null \
    || echo "syslog not readable (user is not in adm)"
else
  echo "syslog not readable (no sg helper)"
fi