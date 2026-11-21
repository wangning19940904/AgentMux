import hashlib
import hmac
import importlib.util
import json
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

spec = importlib.util.spec_from_file_location("meego_todo_poll", Path(__file__).with_name("poll.py"))
poll = importlib.util.module_from_spec(spec)
spec.loader.exec_module(poll)


def todo(work_id, node, name="Login"):
    return {
        "project_key": "p1", "project_name": "Demo",
        "node_info": {"node_name": node, "node_state_key": f"node_{node}_{work_id}"},
        "work_item_info": {"work_item_id": work_id, "work_item_name": name, "work_item_type_key": "story"},
    }


class Receiver(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers["Content-Length"]))
        self.server.requests.append((self.headers.get("X-AgentMux-Signature"), body))
        status = self.server.statuses.pop(0) if self.server.statuses else 202
        self.send_response(status)
        self.end_headers()
        self.wfile.write(b'{"ok":true,"accepted":true}')

    def log_message(self, *args):
        pass


class PollTest(unittest.TestCase):
    def setUp(self):
        self.server = HTTPServer(("127.0.0.1", 0), Receiver)
        self.server.requests, self.server.statuses = [], []
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.config = {
            "callback_url": f"http://127.0.0.1:{self.server.server_port}/hook", "secret": "s3cret", "actor": "meego",
            "site": "https://meego.example.com", "max_events_per_run": 10, "max_pages": 20,
            "state": str(Path(directory.name) / "state.json"),
        }
        self.lists = {"in_progress": [], "not_started": []}
        self.broken = False
        original = poll.read_todos
        self.addCleanup(setattr, poll, "read_todos", original)

        def read(_config, scope):
            if self.broken:
                raise RuntimeError("not signed in")
            return list(self.lists[scope])
        poll.read_todos = read

    def sent(self):
        return [json.loads(body) for _, body in self.server.requests]

    def test_first_run_is_a_baseline_then_only_news_is_sent(self):
        self.config["announce_assigned"] = True
        self.lists["in_progress"] = [todo(1, "dev")]
        self.lists["not_started"] = [todo(2, "dev")]
        self.assertEqual(poll.run(self.config), 0)
        self.assertEqual(self.sent(), [])

        self.lists["not_started"].append(todo(3, "dev", "Payments"))
        self.assertEqual(poll.run(self.config), 0)
        self.assertEqual(poll.run(self.config), 0)  # nothing changed: nothing more
        (assigned,) = self.sent()
        self.assertEqual(assigned["thread"], "p1:3")
        self.assertEqual(assigned["actor"], "meego")
        self.assertEqual(assigned["title"], "Payments")
        self.assertIn(":assigned:", assigned["id"])
        self.assertIn("https://meego.example.com/p1/story/detail/3", assigned["text"])
        signature, body = self.server.requests[0]
        self.assertEqual(signature, "sha256=" + hmac.new(b"s3cret", body, hashlib.sha256).hexdigest())

        # Its turn comes, and so does the turn of one that predates the baseline.
        self.lists["in_progress"] += [todo(3, "dev", "Payments"), todo(2, "dev")]
        self.lists["not_started"] = []
        poll.run(self.config)
        started = self.sent()[1:]
        self.assertEqual(sorted(event["thread"] for event in started), ["p1:2", "p1:3"])
        self.assertTrue(all(":started:" in event["id"] for event in started))

    def test_a_new_work_item_is_one_request_about_its_started_node(self):
        poll.run(self.config)
        nodes = ["plan", "dev", "review", "sandbox", "release"]
        self.lists["in_progress"] = [todo(3, "plan", "Payments"), todo(4, "plan", "Search")]
        self.lists["not_started"] = [todo(3, node, "Payments") for node in nodes[1:]]
        self.lists["not_started"] += [todo(4, node, "Search") for node in nodes[1:]]
        poll.run(self.config)
        poll.run(self.config)
        requests = self.sent()
        self.assertEqual(sorted(event["thread"] for event in requests), ["p1:3", "p1:4"])
        payments = next(event for event in requests if event["thread"] == "p1:3")
        self.assertIn(":started:", payments["id"])
        self.assertIn("node_plan_3", payments["text"])
        self.assertNotIn("node_dev_3", payments["text"], "a node waiting for its turn is not news yet")

        # Two of its nodes start together: one more request names both.
        self.lists["in_progress"] = [todo(3, "dev", "Payments"), todo(3, "review", "Payments"), todo(4, "plan", "Search")]
        self.lists["not_started"] = [todo(3, node, "Payments") for node in nodes[3:]]
        poll.run(self.config)
        (both,) = self.sent()[2:]
        self.assertEqual(both["thread"], "p1:3")
        self.assertIn("2 nodes", both["text"])
        self.assertIn("node_dev_3", both["text"])
        self.assertIn("node_review_3", both["text"])

    def test_with_announce_assigned_waiting_nodes_share_the_request(self):
        self.config["announce_assigned"] = True
        poll.run(self.config)
        self.lists["in_progress"] = [todo(3, "plan")]
        self.lists["not_started"] = [todo(3, "dev"), todo(3, "release")]
        poll.run(self.config)
        (request,) = self.sent()
        self.assertIn(":started:", request["id"])
        self.assertIn("In progress now:\n- plan", request["text"])
        self.assertIn("not started yet:\n- dev", request["text"])
        self.assertIn("- release", request["text"])

    def test_the_run_limit_counts_work_items(self):
        poll.run(self.config)
        self.config["max_events_per_run"] = 2
        self.lists["in_progress"] = [todo(n, "dev") for n in range(1, 4)]
        poll.run(self.config)
        self.assertEqual(len(self.sent()), 2)
        poll.run(self.config)
        self.assertEqual(len(self.sent()), 3)

    def test_a_failed_callback_is_retried_with_the_same_id(self):
        poll.run(self.config)
        self.lists["in_progress"] = [todo(5, "dev")]
        self.server.statuses = [500]
        poll.run(self.config)
        poll.run(self.config)
        first, second = self.sent()
        self.assertEqual(first["id"], second["id"])
        poll.run(self.config)
        self.assertEqual(len(self.sent()), 2)

    def test_a_failed_read_changes_nothing_and_is_reported_once(self):
        self.lists["in_progress"] = [todo(1, "dev")]
        poll.run(self.config)
        self.broken = True
        self.config["report_failures_after"] = 2
        for _ in range(4):
            self.assertEqual(poll.run(self.config), 1)
        (report,) = self.sent()
        self.assertEqual(report["thread"], "poller")
        self.broken = False
        poll.run(self.config)
        self.assertEqual(len(self.sent()), 1, "the to-do from before the outage is not news")

    def test_a_todo_is_forgotten_only_after_several_absent_runs(self):
        self.lists["in_progress"] = [todo(1, "dev")]
        poll.run(self.config)
        self.lists["in_progress"] = []
        poll.run(self.config)
        self.lists["in_progress"] = [todo(1, "dev")]
        poll.run(self.config)
        self.assertEqual(self.sent(), [], "one short read must not announce an old to-do")
        self.lists["in_progress"] = []
        for _ in range(poll.FORGET_AFTER):
            poll.run(self.config)
        self.lists["in_progress"] = [todo(1, "dev")]
        poll.run(self.config)
        self.assertEqual(len(self.sent()), 1, "a node that left and came back is announced")

    def test_dry_run_sends_and_saves_nothing(self):
        poll.run(self.config)
        self.lists["in_progress"] = [todo(9, "dev")]
        poll.run(self.config, dry_run=True)
        self.assertEqual(self.sent(), [])
        poll.run(self.config)
        self.assertEqual(len(self.sent()), 1)


if __name__ == "__main__":
    unittest.main()
