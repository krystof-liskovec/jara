#!/bin/bash
# End-to-end check of `jara --local` without Docker or a real host: a fake
# `ssh` runs "remote" commands locally against a separate data directory and
# emulates the reverse Unix-socket forward; the Jara test binary acts as the
# fake codex/claude CLIs. Scenarios: the laptop runs the agent and checkpoints,
# a phone attaches through the remote, the remote takes over while the laptop
# is offline, the laptop catches up, and a desktop takes over a live laptop.
set -u
REPO=$(cd "$(dirname "$0")/.." && pwd)
R=$(mktemp -d /tmp/jara-runner-e2e.XXXXXX) # short: holds Unix sockets
cleanup() { # only processes of this run: its daemons and fake ssh tunnels
	for pid in $R/*/data/chat/*.pid; do [ -f "$pid" ] && kill -KILL "$(cat "$pid")" 2>/dev/null; done
	pkill -KILL -f "$R/bin/ssh" 2>/dev/null; rm -rf "$R"
}
trap cleanup EXIT
mkdir -p $R/bin $R/hub/home $R/laptop
(cd "$REPO" && go build -o $R/bin/jara ./cmd/jara && go test -c -o $R/bin/fakeagent ./internal/jara) || exit 1
ln -s $R/bin/fakeagent $R/bin/codex; ln -s $R/bin/fakeagent $R/bin/claude
cp "$REPO/scripts/runner_e2e_ssh.py" $R/bin/ssh; chmod +x $R/bin/ssh
printf '#!/bin/sh\nif [ "$1" = exec ]; then shift; [ "$1" = -- ] && shift; exec "$@"; fi\nexit 0\n' > $R/bin/mise; chmod +x $R/bin/mise
export E2E_HUB=$R/hub PATH=$R/bin:$PATH JARA_CODEX_BIN=$R/bin/codex
export JARA_FAKE_AGENT=1 JARA_FAKE_LOG=$R/fake.log JARA_CHAT_PLAIN=1 JARA_CLAUDE_ARGS='[]'
export JARA_DATA=$R/laptop/data JARA_CONFIG=$R/laptop/config.json CODEX_HOME=$R/laptop/codex CLAUDE_CONFIG_DIR=$R/laptop/claude JARA_DEVICE=laptop
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
fail() { echo "FAIL: $*"; exit 1; }
git init -q --bare -b main $R/origin.git
git clone -q $R/origin.git $R/repo 2>/dev/null; cd $R/repo
echo one > a.txt; git add a.txt; git commit -qm init; git push -q origin main 2>/dev/null
echo '{"remotes":{"hub":{"host":"fakehost"}},"default":"hub"}' > $JARA_CONFIG
jara --clone --interface codex --remote hub </dev/null >/dev/null 2>&1
WS=$(ls $R/hub/data/workspaces | grep -v '\.' | head -1); [ -n "$WS" ] || fail "no workspace"
chat() { local first=$1 then=$2; shift 2; (sleep 3; printf '%s\n' "$first"; sleep 5; printf '%s\n' "$then") | timeout 90 "$@" 2>&1; }

echo "== 1. laptop runs the agent, edits, detaches"
echo laptop-edit >> a.txt
chat "hello from laptop" /detach jara --local >/dev/null
sleep 2
grep -q laptop-edit $R/hub/data/workspaces/$WS/a.txt || fail "edit not mirrored"
jara runner status | grep -q "laptop runs the agent .* online" || fail "laptop not online"
jara runner status | grep -q "Last checkpoint.*by laptop" || fail "no laptop checkpoint"

echo "== 2. phone attaches to the live laptop session through the remote"
chat "hello from phone" /detach ssh fakehost "jara connect $WS" | grep -q "codex: hello from laptop" || fail "phone did not see laptop history"
[ -z "$(ls $R/hub/data/chat 2>/dev/null)" ] || fail "remote started its own agent"
tail -1 $R/fake.log | grep -q "hello from phone" || fail "phone turn did not run"

echo "== 3. laptop goes offline; phone takes over"
pid=$(cat $JARA_DATA/chat/local-*.pid); pkill -KILL -P $pid; kill -KILL $pid
pkill -KILL -f "$R/bin/ssh .*ExitOnForwardFailure"; sleep 1
echo offline-edit >> a.txt
chat x /detach ssh fakehost "jara connect $WS" | grep -q "not reachable" || fail "offline laptop not reported"
chat "continue on the remote" /quit ssh fakehost "jara connect --take-over $WS" > $R/takeover.out
grep -q "Taking over from laptop" $R/takeover.out || fail "no takeover"
tail -1 $R/fake.log | grep -q "moved to another machine" || { cat $R/takeover.out; tail -3 $R/fake.log; fail "remote turn was not briefed"; }

echo "== 4. laptop returns, sets aside offline work, catches up"
chat "back on laptop" /quit jara --local > $R/back.out
git stash list | grep -q "set aside" || fail "offline edit not stashed"
grep -q offline-edit a.txt && fail "offline edit still in checkout"
grep -q "codex: continue on the remote" $R/back.out || fail "laptop missing remote turn"
for i in $(seq 20); do jara runner status | grep -q "No device holds" && break; sleep 0.5; done
jara runner status | grep -q "No device holds" || fail "laptop did not hand over on /quit"
jara runner status | grep -q "Last checkpoint.*by laptop" || fail "no final laptop checkpoint"

cd $R/repo
echo "== laptop runs and detaches"
echo laptop-again >> a.txt
chat "laptop turn" /detach jara --local > /dev/null; sleep 2
jara runner status | grep -q "laptop runs the agent .* online" || fail "laptop not online"

echo "== desktop claims; laptop hands over live"
D=$R/desktop; mkdir -p $D
git clone -q $R/origin.git $R/repo2 2>/dev/null
python3 - "$D/config.json" "$R/repo2" "$R/origin.git" "$WS" <<'PY'
import json,sys
cfg,root,origin,ws=sys.argv[1:]
b={"remote":"hub","workspace":ws}
json.dump({"remotes":{"hub":{"host":"fakehost"}},"default":"hub","bindings":{root:b},
  "associations":{"hub\n"+root+"\n"+origin:b}},open(cfg,"w"))
PY
cd $R/repo2
export JARA_DATA=$D/data JARA_CONFIG=$D/config.json CODEX_HOME=$D/codex CLAUDE_CONFIG_DIR=$D/claude JARA_DEVICE=desktop
chat "desktop turn" /quit jara --local > $R/desktop.out
grep -q "Continuing from laptop's checkpoint" $R/desktop.out || { cat $R/desktop.out; fail "desktop did not catch up from laptop"; }
grep -q laptop-again $R/repo2/a.txt || fail "desktop checkout missing laptop edit"
grep -q "codex: laptop turn" $R/desktop.out || fail "desktop missing laptop history"
tail -1 $R/fake.log | grep -q "desktop turn" || fail "desktop turn did not run"
grep -q "Handed the workspace over" $R/laptop/data/chat/local-*.log || fail "laptop log has no handover"
echo PASS
