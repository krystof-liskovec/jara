#!/bin/bash
# End-to-end check of agents running on devices, without Docker or a real
# host: a fake `ssh` runs "remote" commands locally against a separate data
# directory and emulates reverse Unix-socket forwards; the Jara test binary
# acts as the fake codex/claude CLIs. Suspending a device is simulated with
# SIGSTOP/SIGCONT on its processes.
set -u
REPO=$(cd "$(dirname "$0")/.." && pwd)
R=$(mktemp -d /tmp/jara-runner-e2e.XXXXXX) # short: holds Unix sockets
cleanup() { # only processes of this run: daemons, watchers and fake ssh
	for pid in $R/*/data/chat/*.pid $R/hub/data/runners/*.watch; do [ -f "$pid" ] && kill -CONT "$(cat "$pid")" 2>/dev/null && kill -KILL "$(cat "$pid")" 2>/dev/null; done
	pkill -CONT -f "$R/bin" 2>/dev/null; pkill -KILL -f "$R/bin" 2>/dev/null; rm -rf "$R"
}
trap cleanup EXIT
mkdir -p $R/bin $R/hub/home $R/laptop
(cd "$REPO" && go build -o $R/bin/jara ./cmd/jara && go test -c -o $R/bin/fakeagent ./internal/jara) || exit 1
ln -s $R/bin/fakeagent $R/bin/codex; ln -s $R/bin/fakeagent $R/bin/claude
cp "$REPO/scripts/runner_e2e_ssh.py" $R/bin/ssh; chmod +x $R/bin/ssh
printf '#!/bin/sh\nif [ "$1" = exec ]; then shift; [ "$1" = -- ] && shift; exec "$@"; fi\nexit 0\n' > $R/bin/mise; chmod +x $R/bin/mise
export E2E_HUB=$R/hub PATH=$R/bin:$PATH JARA_CODEX_BIN=$R/bin/codex JARA_RUNNER_TIMEOUT=4 JARA_RUNNER_CHECKPOINT=2
export JARA_FAKE_AGENT=1 JARA_FAKE_UNIQUE_THREADS=1 JARA_FAKE_LOG=$R/fake.log JARA_CHAT_PLAIN=1 JARA_CLAUDE_ARGS='[]'
export JARA_DATA=$R/laptop/data JARA_CONFIG=$R/laptop/config.json CODEX_HOME=$R/laptop/codex CLAUDE_CONFIG_DIR=$R/laptop/claude JARA_DEVICE=laptop
export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@t GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@t
fail() {
	echo "FAIL: $*"; tail -5 $R/fake.log
	for f in $R/hub/data/runners/*.json $R/hub/data/runners/*.log $R/hub/data/chat/*.log; do [ -f "$f" ] && { echo "--- $f"; head -c 1500 "$f"; echo; }; done
	[ -n "${KEEP:-}" ] && { echo "kept $R"; trap - EXIT; }
	exit 1
}
status() { (cd $R/repo && jara runner status 2>&1); }
await() { for _ in $(seq 60); do status | grep -q "$1" && return 0; sleep 0.5; done; status; return 1; }
chat() { local first=$1 then=$2; shift 2; (sleep 3; printf '%s\n' "$first"; sleep 5; printf '%s\n' "$then") | timeout 120 "$@" 2>&1; }
# laptop_procs lists the laptop's processes: its session, agents, and its
# side of SSH connections, but not the remote commands the fake ssh runs.
laptop_procs() {
	local daemon child
	daemon=$(cat $JARA_DATA/chat/local-*.pid)
	echo "$daemon"
	for child in $(pgrep -P "$daemon"); do
		echo "$child"
		grep -q "bin/ssh" /proc/$child/cmdline 2>/dev/null || pgrep -P "$child"
	done
}

git init -q --bare -b main $R/origin.git
git clone -q $R/origin.git $R/repo 2>/dev/null; cd $R/repo
echo one > a.txt; git add a.txt; git commit -qm init; git push -q origin main 2>/dev/null
echo '{"remotes":{"hub":{"host":"fakehost"}},"default":"hub"}' > $JARA_CONFIG
jara --clone --interface codex --remote hub </dev/null >/dev/null 2>&1
WS=$(ls $R/hub/data/workspaces | grep -v '\.' | head -1); [ -n "$WS" ] || fail "no workspace"

echo "== 1. jara runs the agent on the laptop by default"
echo laptop-edit >> a.txt
chat "hello from laptop" /detach jara > $R/step1.out; grep -q "runs on this device" $R/step1.out || { cat $R/step1.out; fail "agent did not run locally"; }
grep -q laptop-edit $R/hub/data/workspaces/$WS/a.txt || { cat $R/step1.out; fail "edit not mirrored"; }
await "laptop runs the agent .* online" || fail "laptop not online"

echo "== 2. the phone attaches to the laptop's live session through the remote"
chat "hello from phone" /detach ssh fakehost "jara connect $WS" | grep -q "codex: hello from laptop" || fail "phone did not see laptop history"
[ -z "$(ls $R/hub/data/chat 2>/dev/null)" ] || fail "remote started its own agent"
tail -1 $R/fake.log | grep -q "hello from phone" || fail "phone turn did not run"

echo "== 3. the laptop sleeps mid-turn; the remote continues by itself"
(sleep 3; printf '[slow] long task\n'; sleep 90; printf '/quit\n') | timeout 150 jara > $R/window.out 2>&1 &
WINDOW=$!
sleep 7
grep -q '\[slow\] long task' $R/fake.log || fail "slow turn did not start"
PROCS="$WINDOW $(laptop_procs)"
kill -STOP $PROCS
for _ in $(seq 40); do grep -q "went offline" $R/fake.log && break; sleep 0.5; done
grep -q "went offline" $R/fake.log || fail "remote did not continue the interrupted turn"
status | grep -q "No device holds" || fail "laptop still holds the workspace"

echo "== 4. the laptop wakes, follows the conversation and takes it back"
kill -CONT $PROCS
await "laptop runs the agent .* online" || fail "laptop did not take the workspace back"
sleep 3
grep -q "codex: .*went offline" $R/window.out || { cat $R/window.out; fail "window lacks the remote's continued turn"; }
grep -q "runs on this device" $R/window.out || fail "window did not report the move back"

echo "== 5. the remote is busy when the laptop opens; it hands back after the turn"
kill $WINDOW 2>/dev/null; wait $WINDOW 2>/dev/null
pid=$(cat $JARA_DATA/chat/local-*.pid); kill -TERM "$pid"; sleep 3
await "No device holds" || fail "laptop did not release"
(sleep 3; printf '[slow] remote task\n'; sleep 40; printf '/detach\n') | timeout 90 ssh fakehost "jara connect $WS" > $R/phone.out 2>&1 &
sleep 6
grep -q '\[slow\] remote task' $R/fake.log || fail "remote slow turn did not start"
(sleep 40; printf '/detach\n') | timeout 90 jara > $R/back.out 2>&1
grep -q "you are watching it live" $R/back.out || { cat $R/back.out; fail "laptop did not watch the busy remote"; }
grep -q "codex: \[slow\] remote task" $R/back.out || fail "laptop missed the remote turn"
await "laptop runs the agent .* online" || fail "remote did not hand back after its turn"

echo "== 6. a desktop takes over the live laptop and sees every conversation"
chat "second conversation" /detach jara new > /dev/null
await "laptop runs the agent .* online" || fail "laptop not online"
D=$R/desktop; mkdir -p $D
git clone -q $R/origin.git $R/repo2 2>/dev/null
python3 - "$D/config.json" "$R/repo2" "$R/origin.git" "$WS" <<'PY'
import json,sys
cfg,root,origin,ws=sys.argv[1:]
b={"remote":"hub","workspace":ws}
json.dump({"remotes":{"hub":{"host":"fakehost"}},"default":"hub","bindings":{root:b},
  "associations":{"hub\n"+root+"\n"+origin:b}},open(cfg,"w"))
PY
(
	cd $R/repo2
	export JARA_DATA=$D/data JARA_CONFIG=$D/config.json CODEX_HOME=$D/codex CLAUDE_CONFIG_DIR=$D/claude JARA_DEVICE=desktop
	chat "desktop turn" /quit jara --local > $R/desktop.out
)
grep -q "runs on this device (desktop)" $R/desktop.out || { cat $R/desktop.out; fail "desktop did not take over"; }
grep -q laptop-edit $R/repo2/a.txt || fail "desktop checkout missing laptop edit"
[ "$(ls $D/data/agent-threads | wc -l)" -ge 2 ] || fail "desktop did not receive every conversation"
tail -1 $R/fake.log | grep -q "desktop turn" || fail "desktop turn did not run"

echo "== 7. waiting work moves: the laptop sleeps with a scheduled prompt"
await "No device holds" || fail "desktop did not hand over on /quit"
chat "/schedule in 20s scheduled while asleep" /detach jara > $R/schedule.out
grep -qi "scheduled" $R/schedule.out || { cat $R/schedule.out; fail "prompt was not scheduled"; }
await "laptop runs the agent .* online" || fail "laptop not online"
sleep 2 # a heartbeat reports the waiting prompt
PROCS="$(laptop_procs)"
kill -STOP $PROCS
for _ in $(seq 60); do grep -q "scheduled while asleep" $R/fake.log && break; sleep 0.5; done
kill -CONT $PROCS
grep -q "scheduled while asleep" $R/fake.log || fail "the remote did not run the laptop's scheduled prompt"
echo "== 8. the laptop's agent switches branch; the remote follows at the next checkpoint"
chat "before the switch" /detach jara > /dev/null
await "laptop runs the agent .* online" || fail "laptop not online"
hub_branch() { git -C $R/hub/data/workspaces/$WS symbolic-ref --short HEAD; }
git switch -qc feature; echo two > b.txt; git add b.txt; git commit -qm feature
chat "on feature" /detach jara > $R/feature.out
for _ in $(seq 20); do [ "$(hub_branch)" = feature ] && break; sleep 0.5; done
[ "$(hub_branch)" = feature ] && [ -f $R/hub/data/workspaces/$WS/b.txt ] || { cat $R/feature.out; fail "remote did not follow the branch"; }

echo "== 9. only the remote changed branch; the laptop follows it"
pid=$(cat $JARA_DATA/chat/local-*.pid); kill -TERM "$pid"; sleep 3
await "No device holds" || fail "laptop did not release"
git -C $R/hub/data/workspaces/$WS switch -q main
chat "back on main" /quit jara > $R/main.out
[ "$(git symbolic-ref --short HEAD)" = main ] || { cat $R/main.out; fail "laptop did not follow the remote's branch"; }

echo "== 10. the laptop switched branch; the remote follows and keeps its edits in a stash"
await "No device holds" || fail "laptop did not hand over on /quit"
git switch -q feature; echo remote-only > $R/hub/data/workspaces/$WS/c.txt
chat "feature again" /quit jara > $R/back-feature.out
[ "$(hub_branch)" = feature ] || { cat $R/back-feature.out; fail "remote did not follow the laptop's branch"; }
git -C $R/hub/data/workspaces/$WS stash list | grep -q "jara: work on" || fail "remote edits were not stashed"
echo PASS
