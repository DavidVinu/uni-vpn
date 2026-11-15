"""packaging/release-put.sh against a stand-in for gh that keeps the release in a JSON file."""

import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SCRIPT = ROOT / "packaging" / "release-put.sh"

# The few gh calls release-put.sh makes, on a release kept in $STUB_STATE. Every call is logged
# with the asset names after it. STUB_FAIL_UPLOADS fails that many uploads (after gh's
# --clobber delete, like gh); STUB_LOSE_DELETES deletes but answers with an error.
FAKE_GH = r'''
import json, os, sys
path = os.environ["STUB_STATE"]
state = json.load(open(path))
args = sys.argv[1:]
def save(event):
    state["log"].append([event, sorted(a["name"] for a in state["assets"])])
    json.dump(state, open(path, "w"))
def find(asset_id):
    return [a for a in state["assets"] if str(a["id"]) == asset_id]
if args[:2] == ["release", "upload"]:
    name = os.path.basename(args[3])
    state["assets"] = [a for a in state["assets"] if a["name"] != name]
    if state["fail_uploads"] > 0:
        state["fail_uploads"] -= 1
        save(["upload failed", name])
        sys.exit(1)
    state["next_id"] += 1
    state["assets"].append({"id": state["next_id"], "name": name, "data": open(args[3]).read()})
    save(["upload", name])
elif args[0] == "api" and args[1] == "-X":
    method, url = args[2], args[3]
    asset_id = url.rsplit("/", 1)[1]
    if not find(asset_id):
        sys.exit(1)
    if method == "DELETE":
        name = find(asset_id)[0]["name"]
        state["assets"] = [a for a in state["assets"] if str(a["id"]) != asset_id]
        lost = state["lose_deletes"] > 0
        state["lose_deletes"] -= lost
        save(["delete", name])
        sys.exit(1 if lost else 0)
    new = args[args.index("-f") + 1].split("=", 1)[1]
    if any(a["name"] == new and str(a["id"]) != asset_id for a in state["assets"]):
        sys.exit(1)
    find(asset_id)[0]["name"] = new
    save(["rename", new])
elif args[0] == "api" and args[1].endswith("/tags/latest"):
    print(7)
elif args[0] == "api" and "/7/assets" in args[1]:
    for a in state["assets"]:
        print(a["id"], a["name"])
elif args[0] == "api" and "/assets/" in args[1]:
    sys.exit(0 if find(args[1].rsplit("/", 1)[1]) else 1)
else:
    sys.exit("unexpected gh call: %r" % args)
'''


@unittest.skipIf(os.name == "nt" or not shutil.which("bash"), "a bash script")
class ReleasePutTest(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.dir)
        bin_dir = self.dir / "bin"
        bin_dir.mkdir()
        (bin_dir / "fake_gh.py").write_text(FAKE_GH)
        gh = bin_dir / "gh"
        gh.write_text('#!/bin/sh\nexec "%s" "%s" "$@"\n' % (sys.executable, bin_dir / "fake_gh.py"))
        gh.chmod(0o755)
        self.state = self.dir / "state.json"
        self.env = dict(os.environ, PATH="%s%s%s" % (bin_dir, os.pathsep, os.environ["PATH"]),
                        STUB_STATE=str(self.state), GITHUB_REPOSITORY="o/r", RELEASE_PUT_PAUSE="0")
        self.files = []
        for name in ["a.zip", "core-manifest.json", "uni-vpn.pkg"]:
            path = self.dir / name
            path.write_text("new " + name)
            self.files.append(str(path))

    def put(self, fail_uploads=0, lose_deletes=0, old=("a.zip", "core-manifest.json", "uni-vpn.pkg", "other")):
        assets = [{"id": i, "name": name, "data": "old " + name} for i, name in enumerate(old, 1)]
        self.state.write_text(json.dumps({"assets": assets, "next_id": 100, "log": [],
                                          "fail_uploads": fail_uploads, "lose_deletes": lose_deletes}))
        run = subprocess.run(["bash", str(SCRIPT), "latest", *self.files], env=self.env,
                             capture_output=True, text=True)
        return run, json.loads(self.state.read_text())

    def test_each_file_is_missing_only_between_its_delete_and_rename(self):
        run, state = self.put()
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertEqual({a["name"]: a["data"] for a in state["assets"]},
                         {"a.zip": "new a.zip", "core-manifest.json": "new core-manifest.json",
                          "uni-vpn.pkg": "new uni-vpn.pkg", "other": "old other"})
        log = state["log"]
        # All uploads first, then the swaps in the order given.
        self.assertEqual([e[0] for e, _ in log], ["upload"] * 3 + ["delete", "rename"] * 3)
        self.assertEqual([e[1] for e, _ in log if e[0] == "rename"], ["a.zip", "core-manifest.json", "uni-vpn.pkg"])
        for (event, name), names in log:
            if event == "delete":
                self.assertNotIn(name, names)
            else:
                self.assertTrue({"a.zip", "core-manifest.json", "uni-vpn.pkg", "other"} <= set(names), (event, names))

    def test_a_failed_upload_is_tried_again_and_the_old_file_stays_meanwhile(self):
        run, state = self.put(fail_uploads=2)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertEqual(sorted(a["name"] for a in state["assets"]), ["a.zip", "core-manifest.json", "other", "uni-vpn.pkg"])
        failed = [names for (event, _), names in state["log"] if event == "upload failed"]
        self.assertEqual(len(failed), 2)
        self.assertTrue(all("a.zip" in names for names in failed))

    def test_when_uploads_keep_failing_the_release_keeps_its_old_files(self):
        run, state = self.put(fail_uploads=99)
        self.assertNotEqual(run.returncode, 0)
        self.assertEqual({a["name"]: a["data"] for a in state["assets"]},
                         {"a.zip": "old a.zip", "core-manifest.json": "old core-manifest.json",
                          "uni-vpn.pkg": "old uni-vpn.pkg", "other": "old other"})

    def test_a_lost_answer_to_a_delete_does_not_stop_it(self):
        run, state = self.put(lose_deletes=1)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertEqual(sorted(a["data"] for a in state["assets"] if a["name"] != "other"),
                         ["new a.zip", "new core-manifest.json", "new uni-vpn.pkg"])

    def test_a_new_file_and_one_left_over_from_a_failed_run(self):
        run, state = self.put(old=("next-a.zip", "core-manifest.json"))
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertEqual({a["name"]: a["data"] for a in state["assets"]},
                         {"a.zip": "new a.zip", "core-manifest.json": "new core-manifest.json",
                          "uni-vpn.pkg": "new uni-vpn.pkg"})


if __name__ == "__main__":
    unittest.main()
