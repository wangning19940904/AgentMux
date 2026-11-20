#!/usr/bin/env python3
"""Turn new Meego (Feishu Project) to-dos into requests for a Super Agent.

Meego tells a person about work through its own bot, which no other bot can
read. This script reads the same list instead, the signed-in person's to-dos,
and posts each new one to a Super Agent's custom callback trigger:

- "assigned": a node the person owns appeared, but its turn has not come yet.
- "started": a node the person owns is now in progress.

The first run only records what is already there, so old to-dos start nothing.
Run it every couple of minutes (see README.md). Python 3.9+, standard library.
"""

import argparse
import fcntl
import hashlib
import hmac
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request

PAGE_SIZE = 50
# A to-do must be absent this many polls in a row before it is forgotten, so
# one short read cannot make an old to-do look new again.
FORGET_AFTER = 3
SCOPES = ("in_progress", "not_started")


def log(message):
    print(time.strftime("%Y-%m-%dT%H:%M:%S%z"), message, flush=True)


def load_config(path):
    with open(path, encoding="utf-8") as handle:
        config = json.load(handle)
    for key in ("callback_url", "secret"):
        if not str(config.get(key, "")).strip():
            raise SystemExit(f"{path}: {key} is required")
    config.setdefault("actor", "meego")
    config.setdefault("bytedcli", "bytedcli")
    config.setdefault("site", "https://meego.larkoffice.com")
    config.setdefault("max_events_per_run", 10)
    config.setdefault("max_pages", 20)
    config.setdefault("state", os.path.join(os.path.dirname(os.path.abspath(path)), "state.json"))
    return config


def read_todos(config, scope):
    """Every to-do in one scope, or an exception: a partial list is never used."""
    items = []
    for page in range(1, int(config["max_pages"]) + 1):
        command = [config["bytedcli"], "--json", "meego", "todo", "list",
                   "--action", "todo", "--todo-scope", scope, "--page", str(page)]
        done = subprocess.run(command, capture_output=True, text=True, timeout=120)
        if done.returncode != 0:
            raise RuntimeError(f"bytedcli exited {done.returncode}: {(done.stderr or done.stdout).strip()[:300]}")
        envelope = json.loads(done.stdout)
        if envelope.get("status") != "success":
            raise RuntimeError(f"bytedcli: {json.dumps(envelope.get('error'), ensure_ascii=False)[:300]}")
        result = envelope["data"]["result"]
        if result.get("isError"):
            raise RuntimeError(f"Meego: {json.dumps(result.get('content'), ensure_ascii=False)[:300]}")
        body = json.loads(result["content"][0]["text"])
        batch = body.get("list") or []
        items.extend(batch)
        if len(batch) < PAGE_SIZE or len(items) >= int(body.get("total") or 0):
            return items
    raise RuntimeError(f"more than {config['max_pages']} pages of {scope} to-dos")


def todo_key(item):
    work = item.get("work_item_info") or {}
    node = item.get("node_info") or {}
    sub = item.get("sub_task_info") or {}
    parts = [item.get("project_key", ""), str(work.get("work_item_id", "")), node.get("node_state_key", "")]
    if sub.get("id"):
        parts.append(f"sub{sub['id']}")
    return ":".join(parts)


def describe(config, item, event):
    work = item.get("work_item_info") or {}
    node = item.get("node_info") or {}
    sub = item.get("sub_task_info") or {}
    schedule = item.get("schedule") or {}
    kind = work.get("work_item_type_key") or "story"
    url = f"{config['site'].rstrip('/')}/{item.get('project_key', '')}/{kind}/detail/{work.get('work_item_id', '')}"
    headline = {
        "assigned": "A Meego work item now has a node assigned to you. Its turn has not come yet.",
        "started": "A Meego node assigned to you is now in progress.",
    }[event]
    lines = [
        headline,
        f"Work item: {work.get('work_item_name', '')}",
        f"URL: {url}",
        f"Space: {item.get('project_name', '')} (project_key {item.get('project_key', '')})",
        f"Type: {kind}, work_item_id {work.get('work_item_id', '')}",
        f"Node: {node.get('node_name') or '(unnamed)'} (node_state_key {node.get('node_state_key', '')})",
    ]
    if sub.get("id"):
        lines.append(f"Subtask: {sub.get('name') or '(unnamed)'} (id {sub['id']})")
    if schedule.get("start_time") or schedule.get("end_time"):
        lines.append(f"Schedule: {schedule.get('start_time') or '?'} to {schedule.get('end_time') or '?'}")
    return work.get("work_item_name", ""), "\n".join(lines)


def post(config, payload):
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    signature = hmac.new(config["secret"].encode("utf-8"), body, hashlib.sha256).hexdigest()
    request = urllib.request.Request(config["callback_url"], data=body, method="POST", headers={
        "Content-Type": "application/json", "X-AgentMux-Signature": "sha256=" + signature,
    })
    # The callback is on this machine; a proxy from the environment must not see it.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with opener.open(request, timeout=15) as response:
        return json.loads(response.read() or b"{}")


def load_state(path):
    try:
        with open(path, encoding="utf-8") as handle:
            return json.load(handle)
    except FileNotFoundError:
        return {"baseline": False, "todos": {}, "failures": 0}


def save_state(path, state):
    temporary = path + ".tmp"
    with open(temporary, "w", encoding="utf-8") as handle:
        json.dump(state, handle, ensure_ascii=False, indent=1)
    os.replace(temporary, path)


def plan(state, current, now):
    """Update state["todos"] from the current lists and return the events due.

    current maps key -> (scope, item). An event is (key, event, item)."""
    todos = state["todos"]
    events = []
    for key, (scope, item) in current.items():
        known = todos.get(key)
        if known is None:
            # Before the baseline everything counts as already announced.
            todos[key] = {"scope": scope, "since": now, "missing": 0, "sent": None if state["baseline"] else scope}
            if state["baseline"]:
                events.append((key, "started" if scope == "in_progress" else "assigned", item))
            continue
        known["missing"] = 0
        if known["scope"] != scope:
            known["scope"], known["since"] = scope, now
            if scope == "not_started" and known.get("sent"):
                # Rolled back: its next turn is news again.
                known["sent"] = "not_started"
        # A node whose turn came after it was announced as assigned is news too.
        if scope == "in_progress" and known.get("sent") != "in_progress":
            events.append((key, "started", item))
        elif known.get("sent") is None:
            events.append((key, "assigned", item))
    for key in [key for key in todos if key not in current]:
        todos[key]["missing"] = todos[key].get("missing", 0) + 1
        if todos[key]["missing"] >= FORGET_AFTER:
            del todos[key]
    return events


def run(config, dry_run=False):
    state = load_state(config["state"])
    now = int(time.time())
    try:
        current = {}
        for scope in reversed(SCOPES):  # in_progress last, so it wins a tie
            for item in read_todos(config, scope):
                current[todo_key(item)] = (scope, item)
    except Exception as error:  # the list is unknown: change nothing
        state["failures"] = state.get("failures", 0) + 1
        log(f"read failed ({state['failures']} in a row): {error}")
        # Say so once, after it has clearly stopped being a blip.
        if state["failures"] == int(config.get("report_failures_after", 5)) and not dry_run:
            try:
                post(config, {
                    "id": f"read-failure:{now}", "thread": "poller", "actor": config["actor"],
                    "title": "Meego to-do polling is failing",
                    "text": "The Meego to-do poller on this machine cannot read the to-do list, so new Meego work is not arriving. "
                            "Tell the owner; it usually means `bytedcli meego login` has expired. Do not start other work for this.\n"
                            f"Last error: {error}",
                })
            except Exception as post_error:
                log(f"could not report the failure: {post_error}")
        if not dry_run:
            save_state(config["state"], state)
        return 1
    state["failures"] = 0
    first_run = not state["baseline"]
    events = plan(state, current, now)
    state["baseline"] = True
    if first_run:
        log(f"first run: recorded {len(current)} existing to-dos, nothing sent")
    sent = 0
    for key, event, item in events:
        if sent >= int(config["max_events_per_run"]):
            log(f"{len(events) - sent} more events wait for the next run")
            break
        title, text = describe(config, item, event)
        work_id = (item.get("work_item_info") or {}).get("work_item_id", "")
        entry = state["todos"][key]
        payload = {
            "id": f"{key}:{event}:{entry['since']}", "thread": f"{item.get('project_key', '')}:{work_id}",
            "actor": config["actor"], "title": title, "text": text,
        }
        if dry_run:
            log(f"would send {event}: {title} [{payload['id']}]")
            continue
        try:
            answer = post(config, payload)
        except (urllib.error.URLError, OSError, ValueError) as error:
            log(f"callback failed, will retry next run: {error}")
            break
        entry["sent"] = entry["scope"]
        sent += 1
        log(f"sent {event}: {title} [{payload['id']}] accepted={answer.get('accepted')}")
    if not dry_run:
        save_state(config["state"], state)
    return 0


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--config", default=os.path.expanduser("~/.agentmux/meego-todo/config.json"))
    parser.add_argument("--dry-run", action="store_true", help="read and compare, but send and save nothing")
    args = parser.parse_args()
    config = load_config(args.config)
    lock = open(config["state"] + ".lock", "w")
    try:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        log("another run is still going; skipping")
        return 0
    return run(config, args.dry_run)


if __name__ == "__main__":
    sys.exit(main())
