#!/usr/bin/env python3
"""Land work on origin/main from the shared checkout.

    scripts/land.py -m "<what changed and why>" [-m "<paragraph>" ...] -- <paths...>
    scripts/land.py                       # prepare shared-checkout synchronization
    scripts/land.py --continue <dir>      # land a merge resolved in <dir>
    scripts/land.py --baseline <path> --published
                                          # inspect freshly fetched published bytes for --content
    scripts/land.py --merge-commit <commit>  # publish a prepared upstream merge
    scripts/land.py -m "..." --content <path>=<file> --content-base <path>=<identity> -- <path> ...
                                          # land only your hunks of a shared file

Several agents edit this one working tree at once. `git merge` refuses while a
file it must update has uncommitted edits, and every way around that refusal
(stash, `checkout HEAD -- f`, `git show HEAD:f > f`) silently deletes another
agent's work. This script is the one landing path that never needs to:

1. It captures exactly the named contribution through a private index, using
   recorded local baselines. The shared index and staged changes are untouched.
2. It merges origin/main with `git merge-tree`, which needs no working tree. A
   real conflict is written to a private directory for you to resolve; nothing
   in the checkout moves.
3. It publishes with an ordinary fast-forward push. Publication NEVER changes
   shared working files, HEAD or the index. A receipt is saved before dispatch;
   retry reconciles an uncertain push against origin before doing anything else.
4. Later contributions use the last published capture for each selected path,
   preserving deliberate reversions even while local HEAD remains behind.

Before dispatch, and in `--prepare`, every candidate in a repository carrying a
Concave policy and tools/concave needs a passed parity verdict: one `concave compare --baseline
<origin/main> --candidate <commit>` run by the candidate's own Concave, built from
its tools/concave tree and cached under target/concave/land/<tree>. Concave owns
the scope, the rule and the next action; this script publishes or refuses. A
build failure, an operational failure, an invalid verdict or a timeout refuses too.
`--check-candidate` runs only its checks.

`--check COMMAND` checks each candidate tree in a disposable detached worktree.
Repeat it to run several commands there in order; the first failure refuses
and names its command.
`--check-input PATH` (repeatable) declares every repository path those commands
read. A candidate whose entries at the declared paths, check commands and check
materialization equal those of a candidate whose checks all passed reuses that
pass instead of running them again, so origin/main moving elsewhere while a long
check waits does not repeat it. A pass is recorded under .git/land/checks only
after every command succeeded; a failed, cancelled or interrupted check records
nothing, and changed commands never reuse one. The declaration is the caller's
contract: what a check reads outside the repository (toolchains, caches, the
machine) is not part of the key.
The worktree is only an integration scratch space, never an iteration substrate.
With no paths, prepare a checkout synchronization in a private directory. Resolve
its conflicts there, then `--sync DIR --writers-paused` applies it. This requires
all source writers to be paused or using `--edit -- COMMAND`, which holds the
cooperative writer lock. Fingerprints and a crash journal guard synchronization.
An arbitrary editor which ignores this protocol cannot be made safe by Git.

--content lands a named path with the bytes of <file> instead of the working
file: the working file holds your hunks and someone else's uncommitted ones.
Build FILE from `scripts/land.py --baseline PATH` plus exactly your hunks, not
from a potentially stale HEAD. Pass its printed identity as --content-base
PATH=IDENTITY. If the checkout synchronizes before capture, that recorded
baseline stays the merge base of your delta: newer work is kept, and edits
overlapping it stop as a conflict rather than reverting a peer.
Receipts and recovery refs live under .git/land
and refs/land; do not delete them while checkout synchronization is pending.
A completed synchronization prunes finished journal files itself, and stale
previews and unreferenced conflicts after a day; every refs/land ref stays.

Exit status: 0 published (or synchronization prepared/applied), 3 publication
conflict to resolve, 4 synchronization or pending-capture conflict, 1 other error.
"""
from __future__ import annotations

import argparse
import base64
import fcntl
import hashlib
import json
import os
import re
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time
import uuid
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path

BRANCH = "refs/heads/main"
REMOTE = "origin"
REMOTE_REF = "refs/remotes/origin/main"
MERGE_MESSAGE = "Merge remote-tracking branch 'origin/main'"
ATTEMPTS = 12
IN_PROGRESS = ("MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply")


class Stop(Exception):
    def __init__(self, message: str, code: int = 1):
        super().__init__(message)
        self.code = code


def run(args, *, env=None, data: bytes | None = None, check=True):
    p = subprocess.run(["git", *args], cwd=ROOT, env=env, input=data, capture_output=True)
    if check and p.returncode != 0:
        raise Stop(f"git {' '.join(args)} failed ({p.returncode}):\n{p.stderr.decode(errors='replace').strip()}")
    return p


def git(*args, env=None, data: bytes | None = None) -> str:
    return run(args, env=env, data=data).stdout.decode().strip()


def rev(name: str) -> str:
    return git("rev-parse", "--verify", "-q", f"{name}^{{commit}}")


def is_ancestor(a: str, b: str) -> bool:
    return run(["merge-base", "--is-ancestor", a, b], check=False).returncode == 0


ROOT = Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], capture_output=True, text=True, check=True).stdout.strip())
GIT_DIR = Path(git("rev-parse", "--absolute-git-dir"))
STATE = GIT_DIR / "land"
HASH = hashlib.sha256 if git("rev-parse", "--show-object-format") == "sha256" else hashlib.sha1


# An entry is (mode, oid) as git records it, or None for an absent path.
Entry = tuple[str, str] | None


class PrivateIndex:
    """A throwaway index so the shared one is never read or written."""

    def __enter__(self):
        STATE.mkdir(parents=True, exist_ok=True)
        fd, self.path = tempfile.mkstemp(prefix="index-", dir=STATE)
        os.close(fd)
        os.unlink(self.path)
        self.env = dict(os.environ, GIT_INDEX_FILE=self.path)
        return self

    def __exit__(self, *_):
        for p in (self.path, self.path + ".lock"):
            try:
                os.unlink(p)
            except FileNotFoundError:
                pass


def marker_check(env) -> None:
    p = subprocess.run([str(ROOT / "scripts/conflict-marker-check.sh")], cwd=ROOT, env=env, capture_output=True)
    if p.returncode != 0:
        raise Stop(p.stderr.decode(errors="replace").strip())


def mirror_agents(env) -> None:
    """The pre-commit hook's rule, since commit-tree runs no hooks."""
    agents = run(["rev-parse", "-q", "--verify", ":AGENTS.md"], env=env, check=False).stdout.decode().strip()
    claude = run(["rev-parse", "-q", "--verify", ":CLAUDE.md"], env=env, check=False).stdout.decode().strip()
    if agents and agents != claude:
        git("update-index", "--add", "--cacheinfo", f"100644,{agents},CLAUDE.md", env=env)


def commit_paths(head: str, paths: list[str], message: str, content: dict[str, tuple[Path | None, str]], modes: dict[str, str] | None = None) -> str:
    with PrivateIndex() as ix:
        git("read-tree", head, env=ix.env)
        at_base = entries(head, paths)
        stageable = [p for p in paths if p not in content and (at_base[p] or (ROOT / p).exists() or (ROOT / p).is_symlink())]
        removed = [p for p in stageable if not (ROOT / p).exists() and not (ROOT / p).is_symlink()]
        # git add refuses an explicitly named ignored path even when deleting a
        # tracked file. Record deletions directly in this private index.
        put_entries(ix.env, {p: None for p in removed})
        stageable = [p for p in stageable if p not in removed]
        if stageable:
            git("add", "-A", "--", *stageable, env=ix.env)
        for path, (source, base) in content.items():
            # FILE is based on this contribution's recorded local baseline.
            if run(["rev-parse", "-q", "--verify", f"{head}:{path}"], check=False).stdout.decode().strip() != base:
                raise Stop(f"{path} has a different capture baseline; rebuild --content from --baseline")
            if source is None:
                put_entries(ix.env, {path: None})
                continue
            listed = git("ls-files", "-s", "--", path, env=ix.env)
            mode = (modes or {}).get(path) or (listed.split(" ")[0] if listed else "100644")
            oid = git("hash-object", "-w", "--", str(source))
            git("update-index", "--add", "--cacheinfo", f"{mode},{oid},{path}", env=ix.env)
        mirror_agents(ix.env)
        tree = git("write-tree", env=ix.env)
        if tree == git("rev-parse", f"{head}^{{tree}}"):
            return head
        marker_check(ix.env)
        return git("commit-tree", tree, "-p", head, "-m", message)


@dataclass
class Conflict(Exception):
    ours: str
    theirs: str
    tree: str
    paths: list[str]
    receipt: str | None = None


def integrate(ours: str, theirs: str) -> str:
    """A commit containing both, preferring a fast-forward."""
    if is_ancestor(theirs, ours):
        return ours
    if is_ancestor(ours, theirs):
        return theirs
    p = run(["merge-tree", "--write-tree", "--name-only", "-z", ours, theirs], check=False)
    if p.returncode not in (0, 1):
        raise Stop(f"git merge-tree failed:\n{p.stderr.decode(errors='replace').strip()}")
    fields = p.stdout.decode(errors="surrogateescape").split("\0")
    tree = fields[0]
    if p.returncode == 1:
        paths = []
        for f in fields[1:]:
            if not f:
                break
            if f not in paths:
                paths.append(f)
        raise Conflict(ours, theirs, tree, paths)
    return git("commit-tree", tree, "-p", ours, "-p", theirs, "-m", MERGE_MESSAGE)


def save_conflict(c: Conflict) -> Path:
    d = STATE / time.strftime(f"conflict-%Y%m%dT%H%M%S-{os.getpid()}")
    for path in c.paths:
        target = d / "files" / path
        target.parent.mkdir(parents=True, exist_ok=True)
        p = run(["cat-file", "blob", f"{c.tree}:{path}"], check=False)
        if p.returncode == 0:
            target.write_bytes(p.stdout)
    atomic_json(d / "state.json", {"ours": c.ours, "theirs": c.theirs, "tree": c.tree,
                                    "paths": c.paths, "receipt": c.receipt})
    return d


def resolved_merge(d: Path) -> str:
    s = json.loads((d / "state.json").read_text())
    with PrivateIndex() as ix:
        git("read-tree", s["tree"], env=ix.env)
        for path in s["paths"]:
            f = d / "files" / path
            if f.exists():
                oid = git("hash-object", "-w", "--", str(f))
                listed = git("ls-tree", s["tree"], "--", path)
                mode = listed.split(" ")[0] if listed else "100644"
                git("update-index", "--add", "--cacheinfo", f"{mode},{oid},{path}", env=ix.env)
            else:
                git("update-index", "--force-remove", "--", path, env=ix.env)
        marker_check(ix.env)
        tree = git("write-tree", env=ix.env)
    return git("commit-tree", tree, "-p", s["ours"], "-p", s["theirs"], "-m", MERGE_MESSAGE)


# ---- working tree -------------------------------------------------------

def tree_changes(old: str, new: str) -> list[tuple[str, Entry, Entry]]:
    out = run(["diff-tree", "-r", "-z", "--no-renames", old, new]).stdout.decode(errors="surrogateescape")
    parts = out.split("\0")
    changes = []
    for i in range(0, len(parts) - 1, 2):
        meta, path = parts[i], parts[i + 1]
        if not meta:
            break
        om, nm, oo, no, _ = meta[1:].split(" ")
        changes.append((path, None if om == "000000" else (om, oo), None if nm == "000000" else (nm, no)))
    return changes


def entries(commit: str, paths: list[str]) -> dict[str, Entry]:
    found: dict[str, Entry] = {p: None for p in paths}
    for chunk in range(0, len(paths), 500):
        out = run(["ls-tree", "-z", commit, "--", *paths[chunk:chunk + 500]]).stdout.decode(errors="surrogateescape")
        for rec in filter(None, out.split("\0")):
            meta, path = rec.split("\t", 1)
            mode, _, oid = meta.split(" ")
            found[path] = (mode, oid)
    return found


def blob(oid: str) -> bytes:
    return run(["cat-file", "blob", oid]).stdout


@contextmanager
def batch_blobs():
    """Read immutable blobs one at a time through one owned Git process."""
    child = subprocess.Popen(["git", "cat-file", "--batch"], cwd=ROOT,
                             stdin=subprocess.PIPE, stdout=subprocess.PIPE)
    def read(oid: str) -> bytes:
        if not re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", oid):
            raise Stop("Invalid blob identity")
        child.stdin.write((oid + "\n").encode())
        child.stdin.flush()
        header = child.stdout.readline(256).split()
        if len(header) != 3 or header[0].decode() != oid or header[1] != b"blob":
            raise Stop("Invalid Git blob response during synchronization")
        size = int(header[2])
        data = child.stdout.read(size)
        if len(data) != size or child.stdout.read(1) != b"\n":
            raise Stop("Truncated Git blob during synchronization")
        return data
    try:
        yield read
    finally:
        child.stdin.close()
        child.stdout.close()
        try:
            child.wait(timeout=5)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait()


def oid_of(data: bytes) -> str:
    return HASH(b"blob %d\0" % len(data) + data).hexdigest()


def read_work(path: str) -> tuple[Entry, bytes | None]:
    full = ROOT / path
    try:
        st = os.lstat(full)
    except FileNotFoundError:
        return None, None
    if stat.S_ISLNK(st.st_mode):
        data = os.readlink(full).encode(errors="surrogateescape")
        return ("120000", oid_of(data)), data
    if not stat.S_ISREG(st.st_mode):
        return ("other", ""), None
    data = full.read_bytes()
    return ("100755" if st.st_mode & 0o111 else "100644", oid_of(data)), data


def merge_file(ours: bytes, base: bytes, theirs: bytes) -> bytes | None:
    with tempfile.TemporaryDirectory(dir=STATE) as t:
        files = []
        for name, data in (("ours", ours), ("base", base), ("theirs", theirs)):
            f = os.path.join(t, name)
            Path(f).write_bytes(data)
            files.append(f)
        p = subprocess.run(["git", "merge-file", "-p", *files], capture_output=True)
        return p.stdout if p.returncode == 0 else None


def write_file(path: str, target: Entry, data: bytes | None) -> None:
    full = ROOT / path
    if data is None:
        try:
            os.unlink(full)
        except FileNotFoundError:
            pass
        parent = full.parent
        while parent != ROOT:
            try:
                parent.rmdir()
            except OSError:
                break
            parent = parent.parent
        return
    full.parent.mkdir(parents=True, exist_ok=True)
    tmp = full.parent / f".{full.name}.land-{os.getpid()}"
    if target[0] == "120000":
        os.symlink(data.decode(errors="surrogateescape"), tmp)
    else:
        tmp.write_bytes(data)
        os.chmod(tmp, 0o755 if target[0] == "100755" else 0o644)
    os.replace(tmp, full)


# ---- the landing cycle --------------------------------------------------

def check_quiet_checkout() -> None:
    for name in IN_PROGRESS:
        if (GIT_DIR / name).exists():
            raise Stop(f"a git operation is in progress in this checkout ({name}); whoever started it must finish it first")
    if git("symbolic-ref", "-q", "HEAD") != BRANCH:
        raise Stop("HEAD is not main")


def atomic_json(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(dir=path.parent, prefix=".state-")
    try:
        with os.fdopen(fd, "w") as f:
            json.dump(value, f, indent=2)
            f.flush()
            os.fsync(f.fileno())
        os.replace(name, path)
        fd = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
    finally:
        if os.path.exists(name):
            os.unlink(name)


@contextmanager
def locked(name: str, exclusive: bool = True):
    STATE.mkdir(parents=True, exist_ok=True)
    with open(STATE / name, "a") as f:
        fcntl.flock(f, fcntl.LOCK_EX if exclusive else fcntl.LOCK_SH)
        yield f


@contextmanager
def unlocked(lock):
    if lock is not None:
        fcntl.flock(lock, fcntl.LOCK_UN)
    try:
        yield
    finally:
        if lock is not None:
            fcntl.flock(lock, fcntl.LOCK_EX)


@contextmanager
def publication_guard(directory: Path):
    with open(directory / "publication.lock", "a") as f:
        try:
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Stop(f"This capture is already being published: {directory}", 4)
        yield


def records() -> list[tuple[Path, dict]]:
    result = [(p.parent, json.loads(p.read_text()))
              for p in (STATE / "receipts").glob("*/state.json")]
    return sorted(result, key=lambda item: item[1]["sequence"])


def publication_status(identity: str) -> dict:
    """Public, observational protocol. Never fetch, reconcile, or create state."""
    directory = STATE / "receipts" / identity
    result = {"format": 1, "kind": "land-publication", "id": identity,
              "state": "unpublished", "receipt": str(directory)}
    path = directory / "state.json"
    if not path.exists():
        return result
    if path.stat().st_size > 16 * 1024 * 1024:
        raise Stop("Publication receipt exceeds 16 MiB")
    record = json.loads(path.read_text())
    if record.get("kind") != "publication":
        raise Stop("Invalid publication receipt")
    if record.get("published"):
        result["state"] = "published"
        result["commit"] = record["candidate"]
    elif record.get("abandoned"):
        result["state"] = "abandoned"
    elif record.get("conflict"):
        result["state"] = "conflict"
        result["conflict"] = record["conflict"]
    else:
        result["state"] = "unconfirmed" if record.get("dispatched") else "pending"
    return result


def save_record(directory: Path, record: dict) -> None:
    # Ref protection precedes the durable receipt and any network dispatch.
    for key in ("base", "capture", "candidate", "resolved"):
        if record.get(key):
            git("update-ref", f"refs/land/receipts/{directory.name}/{key}", record[key])
    atomic_json(directory / "state.json", record)


def reconcile(remote: str) -> None:
    for directory, record in records():
        if not record["published"] and record.get("dispatched") and record.get("candidate") and is_ancestor(record["candidate"], remote):
            record["published"] = True
            save_record(directory, record)


def fetch(lock=None) -> str:
    with unlocked(lock):
        # Network waits release the publication lock. A peer fetch can advance
        # this same ref after Git reads its old value but before it updates it.
        # Retry that compare-and-swap race only; persistent/network failures
        # retain their original diagnostic and never become assumed success.
        for attempt in range(3):
            try:
                git("fetch", "-q", REMOTE, f"{BRANCH}:{REMOTE_REF}")
                break
            except Stop as error:
                raced = re.search(
                    rf"cannot lock ref '{re.escape(REMOTE_REF)}': is at [0-9a-f]+ but expected [0-9a-f]+",
                    str(error),
                )
                if not raced or attempt == 2:
                    raise
    remote = rev(REMOTE_REF)
    reconcile(remote)
    return remote


def baselines(head: str, paths: list[str]) -> dict[str, Entry]:
    result = entries(head, paths)
    for _, record in records():
        if not record["published"]:
            continue
        selected = sorted(set(paths) & set(record["paths"]))
        if not selected or is_ancestor(record["candidate"], head):
            continue
        if record["checkout_head"] != head:
            raise Stop("Local HEAD moved outside recorded synchronization; reconcile the checkout before capturing more work", 4)
        result.update(entries(record["capture"], selected))
    return result


def put_entries(env: dict, values: dict[str, Entry]) -> None:
    # Rewrite the private index once, including paths containing tabs or newlines.
    # Per-path processes make a large refresh lose its publication basis repeatedly.
    records = []
    for path, entry in values.items():
        mode, oid = entry if entry else ("0", "0" * (HASH().digest_size * 2))
        records.append(f"{mode} {oid}\t{path}\0".encode(errors="surrogateescape"))
    if records:
        git("update-index", "-z", "--index-info", env=env, data=b"".join(records))


def capture(paths: list[str], message: str, content: dict[str, Path | None], check: list[str] | None,
            content_bases: dict[str, str] | None = None, *, persist=True,
            content_sha256: dict[str, str] | None = None,
            content_modes: dict[str, str] | None = None,
            publication_id: str | None = None) -> tuple[Path, dict] | None:
    if publication_id and (STATE / "receipts" / publication_id).exists():
        raise Stop(f"Publication already captured; resume with --continue {STATE / 'receipts' / publication_id}", 4)
    head = rev("HEAD")
    # Expand directories, including tracked deletions, without including ignored output.
    listed = run(["ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", *paths]).stdout
    requested = paths
    historical = {p for _, r in records() for p in r["paths"]
                  if any(p == q or p.startswith(q.rstrip("/") + "/") for q in requested)}
    paths = sorted(set(filter(None, listed.decode().split("\0"))) | set(content) | historical)
    # A previously published deletion is no longer in ls-files after synchronization.
    if not paths:
        raise Stop("No selected source paths")
    for directory, record in records():
        if persist and not record["published"] and not record.get("abandoned") and set(paths) & set(record["paths"]):
            raise Stop(f"An unfinished capture owns overlapping paths; resume its frozen input: scripts/land.py --continue {directory}", 4)
    baseline = baselines(head, paths)
    # Reviewed bytes are the recorded baseline plus selected edits. When the
    # checkout has synchronized since review, that recorded baseline is the
    # merge base of the delta, so merging with the remote keeps the newer work
    # and overlapping edits stop as a conflict; nothing is reverted.
    reviewed: dict[str, Entry] = {}
    for path in content:
        identity = (content_bases or {}).get(path)
        if identity is None:
            raise Stop(f"{path}: --content needs its --content-base identity from --baseline", 4)
        if identity != entry_identity(baseline[path]):
            reviewed[path] = identity_entry(path, identity)
    effective = {**baseline, **reviewed}
    with locked("workspace.lock", exclusive=False):
        require_no_sync_in_progress()
        for p in paths:
            safe_parent(p)
        before = {p: read_work(p)[0] for p in paths}
        content_before = {p: fingerprint(f.read_bytes()) if f is not None else None for p, f in content.items()}
        for path, expected in (content_sha256 or {}).items():
            if content_before[path] != expected:
                raise Stop(f"{path}: --content bytes differ from their reviewed SHA256", 4)
        with PrivateIndex() as ix:
            git("read-tree", head, env=ix.env)
            put_entries(ix.env, effective)
            if "AGENTS.md" in paths:
                mirror_agents(ix.env)
            base_tree = git("write-tree", env=ix.env)
            base = git("commit-tree", base_tree, "-p", head, "-m", "Local contribution baseline (private)")
        content_args = {p: (f, e[1] if e else "") for p, f in content.items() for e in [effective[p]]}
        # A working file still at the pre-image of a private publication holds no
        # edit; capturing it would publish a reversion of that publication. It
        # keeps its publication baseline, already in base, until synchronization.
        stale = stale_preimages({p: e for p, e in before.items() if p not in content and e != baseline[p]
                                 and (e is None or e[0] != "other")})
        if stale:
            print(f"Not publishing {len(stale)} working path(s) left at the pre-image of a private publication "
                  "(synchronize to advance them): " + ", ".join(sorted(stale)), file=sys.stderr)
        captured = commit_paths(base, [p for p in paths if p not in stale], message, content_args, content_modes)
        if before != {p: read_work(p)[0] for p in paths} or content_before != {
                p: fingerprint(f.read_bytes()) if f is not None else None for p, f in content.items()}:
            raise Stop("Selected input changed during capture; retry without publishing", 4)
        if entries(captured, paths) == effective:
            return None
        if rev("HEAD") != head:
            raise Stop("HEAD changed during capture; retry without publishing")
    directory = STATE / "receipts" / (publication_id or uuid.uuid4().hex)
    record = {"kind": "publication", "sequence": max((r["sequence"] for _, r in records()), default=0) + 1,
              "checkout_head": head, "base": base, "capture": captured, "paths": paths,
              "message": message, "check": check, "published": False}
    if persist:
        save_record(directory, record)
    return directory, record


def capture_upstream_merge(commit: str, remote: str, check: list[str] | None) -> tuple[Path, dict]:
    """Adopt only a merge of published main and known upstream ancestry."""
    commit = rev(commit)
    parents = git("rev-list", "--parents", "-n", "1", commit).split()[1:]
    if len(parents) != 2 or not is_ancestor(parents[0], remote):
        raise Stop("Prepared upstream merge must have two parents and a published main first parent")
    if not is_ancestor(parents[1], rev("refs/remotes/upstream/main")):
        raise Stop("Prepared upstream merge second parent must belong to upstream/main")
    directory = STATE / "receipts" / uuid.uuid4().hex
    record = {"kind": "publication", "sequence": max((r["sequence"] for _, r in records()), default=0) + 1,
              "checkout_head": rev("HEAD"), "capture": commit, "upstream_merge": True,
              "paths": [], "message": git("log", "-1", "--format=%B", commit),
              "check": check, "published": False}
    save_record(directory, record)
    return directory, record


def candidate(record: dict, remote: str) -> str:
    if record.get("upstream_merge"):
        return integrate(record.get("resolved") or record["capture"], remote)
    if record.get("resolved"):
        # A private resolution has capture and remote parents. Publish its
        # resolved delta against that remote, never its private ancestry.
        capture = record["resolved"]
        base = rev(capture + "^2")
    else:
        capture, base = record["capture"], record["base"]
    p = run(["merge-tree", "--write-tree", "--name-only", "-z", "--merge-base=" + base,
             capture, remote], check=False)
    if p.returncode not in (0, 1):
        raise Stop(p.stderr.decode(errors="replace"))
    fields = p.stdout.decode().split("\0")
    tree = fields[0]
    if p.returncode:
        paths = fields[1:fields.index("", 1)]
        raise Conflict(capture, remote, tree, list(dict.fromkeys(paths)))
    if tree == git("rev-parse", f"{remote}^{{tree}}"):
        return remote
    # Only the selected delta reaches published history, not the synthetic baseline.
    return git("commit-tree", tree, "-p", remote, "-m", record["message"])


CHECK_RECORD_DAYS = 14
# Child runners may spend five seconds stopping their command, then drain owned
# databases. The outer deadline must leave that cleanup its own bounded window.
CHECK_SHUTDOWN_SECONDS = 20
MAX_CHECK_INPUTS = 2000


def check_identity(commit: str, commands: list[str], inputs: list[str], excludes: list[str] | None,
                   paths: list[str] | None) -> dict:
    """What a check's verdict depends on: commands, materialization, declared entries."""
    declared = sorted({path.rstrip("/") for path in inputs})
    if len(declared) > MAX_CHECK_INPUTS:
        raise Stop(f"--check-input exceeds {MAX_CHECK_INPUTS} paths")
    # One path per listing: ls-tree descends into a directory when another
    # selected path lies beneath it, and would then omit the directory entry.
    found = {path: entry_identity(entries(commit, [path])[path]) for path in declared}
    material = {"format": 1, "commands": list(commands), "inputs": found,
                "materialization": {"paths": sorted(paths or []), "excludes": sorted(excludes or [])}}
    return {"key": fingerprint(json.dumps(material, sort_keys=True).encode()), **material}


def passed_check(identity: dict) -> dict | None:
    try:
        record = json.loads((STATE / "checks" / (identity["key"] + ".json")).read_text())
    except (FileNotFoundError, ValueError):
        return None
    # The file name is a digest; reuse only the identical recorded material.
    if not isinstance(record, dict) or any(record.get(name) != identity[name]
                                           for name in ("key", "format", "commands", "inputs", "materialization")):
        return None
    return record


def record_passed_check(identity: dict, commit: str) -> None:
    directory = STATE / "checks"
    atomic_json(directory / (identity["key"] + ".json"),
                {**identity, "candidate": commit, "tree": git("rev-parse", f"{commit}^{{tree}}"),
                 "passed_at": time.time()})
    horizon = time.time() - CHECK_RECORD_DAYS * 86400
    for old in directory.glob("*.json"):
        try:
            if old.stat().st_mtime < horizon:
                old.unlink()
        except FileNotFoundError:
            pass


def materialized_source_times(checkout: Path, commit: str, commands: list[str],
                              excludes: list[str] | None, paths: list[str] | None) -> dict:
    """Record only tracked inputs of a verified disposable checkout."""
    def tree(kind: str) -> dict:
        rows = git("ls-tree", "-r", "-z", *(["-d"] if kind == "tree" else []), commit).split("\0")
        return {path: (mode, oid) for row in rows if row
                for header, path in [row.split("\t", 1)]
                for mode, entry_kind, oid in [header.split()] if entry_kind == kind}

    blobs, directories = tree("blob"), tree("tree")
    def times(entries: dict) -> dict:
        result = {}
        for path, entry in entries.items():
            full = checkout / path
            try:
                info = full.lstat()
            except FileNotFoundError:
                continue  # Sparse materialization retains only its declared sources.
            result[path] = [*entry, info.st_mtime_ns]
        return result
    context = {path: list(entry) for path, entry in blobs.items()
               if path.endswith("Cargo.toml") or path in ("Cargo.lock", "rust-toolchain", "rust-toolchain.toml")
               or path.startswith(".cargo/")}
    return {"format": 1, "commands": commands, "context": context,
            "materialization": {"paths": sorted(paths or []), "excludes": sorted(excludes or [])},
            "files": times(blobs), "directories": times(directories)}


def reuse_source_times(checkout: Path, current: dict, cached: Path) -> int:
    """Reuse mtimes only for identical tracked bytes and build configuration.

    Cargo's build-script rerun-if-changed checks use timestamps even with
    checksum freshness. Changed files and their directory trees retain their
    fresh checkout timestamps; no build result or check verdict is reused here.
    """
    try:
        prior = json.loads(cached.read_text())
    except (OSError, ValueError):
        return 0
    if (not isinstance(prior, dict) or any(prior.get(key) != current[key]
                                          for key in ("format", "commands", "context", "materialization"))):
        return 0
    restored = 0
    now = time.time_ns()
    for kind in ("files", "directories"):
        entries = prior.get(kind)
        if not isinstance(entries, dict):
            continue
        for path, entry in current[kind].items():
            old = entries.get(path)
            if (not isinstance(old, list) or len(old) != 3 or old[:2] != list(entry[:2])
                    or type(old[2]) is not int or not 0 <= old[2] <= now):
                continue
            full = checkout / path
            info = full.lstat()
            os.utime(full, ns=(info.st_atime_ns, old[2]), follow_symlinks=False)
            entry[2] = old[2]
            restored += 1
    return restored


def check_candidate(commit: str, commands: list[str] | None, excludes: list[str] | None = None,
                    paths: list[str] | None = None, inputs: list[str] | None = None,
                    scratch_key: str | None = None) -> None:
    with PrivateIndex() as ix:
        git("read-tree", commit, env=ix.env)
        marker_check(ix.env)
    # An empty command checks nothing, as an empty --check always has.
    commands = [command for command in commands or [] if command]
    if not commands:
        return
    identity = None
    if inputs:
        identity = check_identity(commit, commands, inputs, excludes, paths)
        prior = passed_check(identity)
        if prior is not None:
            print(f"Candidate check of {commit[:12]} reuses the pass of {str(prior.get('candidate'))[:12]}: "
                  f"identical commands and {len(identity['inputs'])} declared input(s)", flush=True)
            return
    # Jest ignores every path containing .git, including a validation checkout.
    if scratch_key is None:
        scratch = Path(tempfile.mkdtemp(prefix="bijection-land-check-"))
    else:
        # publication_guard already serializes this receipt. Recreate an empty
        # disposable checkout at the same path on retry so content-aware Cargo
        # freshness and compiler caches can reuse actual unchanged inputs.
        scratch = Path(tempfile.gettempdir()) / ("bijection-land-check-" + fingerprint(scratch_key.encode())[:32])
        try:
            scratch.mkdir()
        except FileExistsError:
            raise Stop(f"An earlier check remains at {scratch}; inspect its owned processes and recover it before retrying", 4)
    checkout = scratch / "source"
    child = None
    cancelled_at = None
    source_times = None
    time_cache = STATE / "check-materialization" / (fingerprint(scratch_key.encode()) + ".json") if scratch_key else None

    def cancel_check(signum, frame):
        nonlocal cancelled_at
        if cancelled_at is not None:
            return
        cancelled_at = time.monotonic()
        if child is None:
            raise Stop("Candidate check cancelled; nothing published")
        # The check has its own process group; target only this exact child.
        try:
            os.killpg(child.pid, signal.SIGTERM)
        except ProcessLookupError:
            pass

    def stop_group():
        # A successful shell may still have background children.
        try:
            os.killpg(child.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass

    previous_signals = {sig: signal.signal(sig, cancel_check) for sig in (signal.SIGTERM, signal.SIGINT)}
    try:
        options = ["--no-checkout"] if excludes or paths else []
        git("worktree", "add", "--quiet", "--detach", *options, str(checkout), commit)
        if excludes or paths:
            # Only this disposable checkout's index and sparse specification change.
            # Do not enable repository-wide worktree configuration or change the
            # shared checkout's index. These are explicit check inputs, not defaults.
            spec = subprocess.run(["git", "rev-parse", "--git-path", "info/sparse-checkout"],
                                  cwd=checkout, capture_output=True, text=True, check=True).stdout.strip()
            sparse = Path(spec)
            if not sparse.is_absolute():
                sparse = checkout / sparse
            sparse.parent.mkdir(parents=True, exist_ok=True)
            sparse.write_text("".join(f"/{path}\n" for path in paths) if paths else
                              "/*\n" + "".join(f"!/{path}/\n" for path in excludes))
            subprocess.run(["git", "-c", "core.sparseCheckout=true",
                            "-c", "core.sparseCheckoutCone=false", "read-tree", "-mu", commit],
                           cwd=checkout, check=True)
            print("Check materialization: " + (", ".join(paths) if paths else "excluding " + ", ".join(excludes)), flush=True)
        if time_cache is not None:
            current = materialized_source_times(checkout, commit, commands, excludes, paths)
            restored = reuse_source_times(checkout, current, time_cache)
            source_times = current
            if restored:
                print(f"Reused timestamps for {restored} unchanged tracked inputs; all checks still run", flush=True)
        # Commands share this checkout in order. Each must leave the candidate's
        # sources as it found them, so every later command checks the same tree.
        for number, command in enumerate(commands, 1):
            if cancelled_at is not None:
                raise Stop("Candidate check cancelled; nothing published")
            step = f" {number}/{len(commands)}" if len(commands) > 1 else ""
            name = f"Candidate check{step} `{command}`"
            print(f"Candidate check{step} of {commit[:12]} in temporary checkout: {command}", flush=True)
            child = subprocess.Popen(command, shell=True, cwd=checkout,
                                     env=dict(os.environ, LAND_CANDIDATE_CHECK="1"), start_new_session=True)
            while True:
                try:
                    code = child.wait(timeout=0.2)
                    break
                except subprocess.TimeoutExpired:
                    if cancelled_at is not None and time.monotonic() - cancelled_at >= CHECK_SHUTDOWN_SECONDS:
                        os.killpg(child.pid, signal.SIGKILL)
            if cancelled_at is not None:
                # The shell can exit before a cooperative child has saved its
                # receipt. Leave that exact group a bounded cleanup window too.
                while time.monotonic() - cancelled_at < CHECK_SHUTDOWN_SECONDS:
                    try:
                        os.killpg(child.pid, 0)
                    except ProcessLookupError:
                        break
                    time.sleep(0.02)
                raise Stop("Candidate check cancelled; nothing published")
            stop_group()
            child = None
            if code:
                raise Stop(f"{name} failed ({code}); nothing published")
            if any((checkout / path).exists() or (checkout / path).is_symlink() for path in excludes or []):
                raise Stop(f"{name} populated an excluded directory; nothing published")
            if paths:
                skipped = subprocess.run(["git", "ls-files", "-t", "-z"], cwd=checkout,
                                         capture_output=True, check=True).stdout.decode().split("\0")
                if any((checkout / entry[2:]).exists() or (checkout / entry[2:]).is_symlink()
                       for entry in skipped if entry.startswith("S ")):
                    raise Stop(f"{name} populated source outside declared materialization; nothing published")
            if git("rev-parse", f"{commit}^{{tree}}") != subprocess.run(
                    ["git", "write-tree"], cwd=checkout, capture_output=True, text=True, check=True).stdout.strip():
                raise Stop(f"{name} changed its index; nothing published")
            changed = subprocess.run(["git", "diff", "--quiet"], cwd=checkout).returncode
            if changed:
                raise Stop(f"{name} changed tracked sources; nothing published")
        # Preserve the timestamps seen before checks. A generator may touch an
        # identical tracked input after a build; that later timestamp is not a
        # change in its verified bytes and must not invalidate the next retry.
    finally:
        try:
            if child is not None:
                stop_group()
            if checkout.exists():
                git("worktree", "remove", "--force", str(checkout))
            shutil.rmtree(scratch)
        finally:
            for sig, handler in previous_signals.items():
                signal.signal(sig, handler)
    # Reached only when every command passed and the checkout was removed.
    if time_cache is not None and source_times is not None:
        atomic_json(time_cache, source_times)
    if identity is not None:
        record_passed_check(identity, commit)


def cleanup_check(directory: str, commit: str) -> None:
    """Remove an interrupted check after its owned processes have stopped.

    Bind recovery to an unchanged detached candidate in this repository's
    normal validation directory. Never remove a shared or edited checkout.
    """
    supplied = Path(directory)
    checkout = supplied.resolve()
    scratch = checkout.parent
    if (supplied.is_symlink() or checkout.name != "source"
            or not scratch.name.startswith("bijection-land-check-")
            or scratch.parent != Path(tempfile.gettempdir()).resolve()):
        raise Stop("Cleanup requires an exact temporary landing-check checkout")
    candidates = git("worktree", "list", "--porcelain", "-z").split("\0\0")
    expected = rev(commit)
    entry = next((item.split("\0") for item in candidates
                  if item.startswith("worktree " + str(checkout) + "\0")), None)
    if entry is None or "detached" not in entry or "HEAD " + expected not in entry:
        raise Stop("Cleanup requires the exact registered detached candidate")
    if any(path != checkout for path in scratch.iterdir()):
        raise Stop("Unexpected files beside the validation checkout; preserve them first")
    initializing = "locked initializing" in entry
    if initializing:
        # Git can stop during materialization, leaving an incomplete index and
        # its initialization lock. Admit only bytes of the exact candidate;
        # changed or untracked files still need their owner's recovery.
        present = []
        for parent, dirs, files in os.walk(checkout, followlinks=False):
            paths = [Path(parent) / name for name in files]
            paths += [Path(parent) / name for name in dirs if (Path(parent) / name).is_symlink()]
            for path in paths:
                relative = path.relative_to(checkout).as_posix()
                if relative != ".git": present.append(relative)
        original = entries(expected, present)
        for path in present:
            full = checkout / path
            st = full.lstat()
            if full.is_symlink():
                actual = ("120000", oid_of(os.readlink(full).encode(errors="surrogateescape")))
            elif stat.S_ISREG(st.st_mode):
                digest = HASH(b"blob %d\0" % st.st_size)
                with full.open("rb") as source:
                    for block in iter(lambda: source.read(1024 * 1024), b""): digest.update(block)
                actual = ("100755" if st.st_mode & 0o111 else "100644", digest.hexdigest())
            else:
                actual = None
            if original[path] is None or original[path] != actual:
                raise Stop("Initializing validation checkout contains source edits; preserve them first")
    else:
        for args in (["diff", "--quiet"], ["diff", "--cached", "--quiet"]):
            if subprocess.run(["git", "-C", str(checkout), *args]).returncode:
                raise Stop("Validation checkout contains source edits; preserve them first")
    git("worktree", "remove", "--force", *(["--force"] if initializing else []), str(checkout))
    scratch.rmdir()
    print("Removed interrupted validation checkout " + str(checkout))


# ---- Concave upstream parity -----------------------------------------------
#
# Concave decides; land.py publishes or refuses. One command, the candidate's own
# Concave, compares the candidate with the published commit it builds on and
# returns one verdict: regressions, coverage loss and the next action. When the
# baseline's Concave source differs, its own build measures the coverage the
# candidate may not lose. Each is built from its commit's tools/concave tree.

CONCAVE_POLICY = "concave.json"
CONCAVE_SOURCE = "tools/concave"
CONCAVE_GO = "go1.26.8"
CONCAVE_BUILD_TIMEOUT = 600
CONCAVE_CHECK_TIMEOUT = 600
CONCAVE_CACHE_DAYS = 14


def bounded(argvs: list[list[str]], what: str, timeout: int, **kwargs) -> list[subprocess.CompletedProcess]:
    """Run owned process groups concurrently to completion, one deadline or cancellation."""
    children = []

    def cancel(signum, frame):
        raise Stop(f"{what} cancelled; nothing published")

    previous = {sig: signal.signal(sig, cancel) for sig in (signal.SIGTERM, signal.SIGINT)}
    try:
        deadline = time.monotonic() + timeout
        for argv in argvs:
            # Files, not pipes: a large report never stalls its concurrent peer.
            out, err = tempfile.TemporaryFile(), tempfile.TemporaryFile()
            try:
                children.append((subprocess.Popen(argv, stdout=out, stderr=err, start_new_session=True, **kwargs), out, err))
            except OSError as error:
                out.close()
                err.close()
                raise Stop(f"{what} could not start: {error}")
        for child, _, _ in children:
            try:
                child.wait(timeout=max(0, deadline - time.monotonic()))
            except subprocess.TimeoutExpired:
                raise Stop(f"{what} exceeded {timeout}s; nothing published")
        results = []
        for child, out, err in children:
            out.seek(0)
            err.seek(0)
            results.append(subprocess.CompletedProcess(child.args, child.returncode, out.read(), err.read()))
        return results
    finally:
        for child, out, err in children:
            if child.poll() is None:
                # Only this exact group: the child and anything it started.
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                child.wait()
            out.close()
            err.close()
        for sig, handler in previous.items():
            signal.signal(sig, handler)


def concave_executable(commit: str) -> Path:
    """Concave built from the commit's own tools/concave tree, cached by that tree."""
    tree = concave_source(commit)
    if not tree:
        raise Stop(f"Concave parity: {commit[:12]} has a Concave policy but no {CONCAVE_SOURCE}; nothing published")
    cache = ROOT / "target/concave/land"
    exe = cache / tree / "concave"
    if exe.is_file() and os.access(exe, os.X_OK):
        os.utime(exe.parent)
        return exe
    cache.mkdir(parents=True, exist_ok=True)
    scratch = Path(tempfile.mkdtemp(prefix=".build-", dir=cache))
    try:
        source = scratch / "source"
        source.mkdir()
        archive = run(["archive", "--format=tar", tree]).stdout
        subprocess.run(["tar", "-x", "-C", str(source)], input=archive, check=True)
        output = scratch / "out"
        [built] = bounded([["go", "build", "-trimpath", "-o", str(output / "concave"), "./cmd/concave"]],
                          f"Building Concave {tree[:12]}", CONCAVE_BUILD_TIMEOUT, cwd=source,
                          env=dict(os.environ, GOTOOLCHAIN=CONCAVE_GO, GOFLAGS="-mod=readonly", GOWORK="off"))
        if built.returncode:
            raise Stop(f"Concave parity: building {CONCAVE_SOURCE} at {commit[:12]} failed ({built.returncode}); "
                       "nothing published. It needs Go (see DEVELOPMENT.md, `just concave-build`):\n"
                       + built.stderr.decode(errors="replace")[-4000:].strip())
        try:
            os.rename(output, cache / tree)
        except OSError:
            if not exe.is_file():  # Otherwise a concurrent landing installed the same tree.
                raise
    finally:
        shutil.rmtree(scratch, ignore_errors=True)
    horizon = time.time() - CONCAVE_CACHE_DAYS * 86400
    for old in cache.iterdir():
        if old.name != tree and not old.name.startswith(".") and old.stat().st_mtime < horizon:
            shutil.rmtree(old, ignore_errors=True)
    return exe


def concave_source(commit: str) -> str | None:
    source = entries(commit, [CONCAVE_SOURCE])[CONCAVE_SOURCE]
    return source[1] if source and source[0] == "040000" else None


def check_parity(base: str, new: str) -> None:
    """Publish only on the candidate Concave's passed verdict against base."""
    if new == base:
        return
    started = time.monotonic()
    policies = {commit: entries(commit, [CONCAVE_POLICY])[CONCAVE_POLICY] for commit in (base, new)}
    sources = {commit: concave_source(commit) for commit in (base, new)}
    # Only a repository carrying both a policy and Concave's source is judged;
    # once either commit carries them, the candidate must build its own Concave.
    if not any(policies.values()) or not any(sources.values()):
        return
    executable = concave_executable(new)
    argv = [str(executable), "--repo", str(ROOT), "compare", "--baseline", base, "--candidate", new, "--json"]
    if policies[base] and sources[base]:
        baseline = concave_executable(base)
        if baseline != executable:
            argv += ["--baseline-tool", str(baseline)]
    [p] = bounded([argv], f"Concave parity of {new[:12]}", CONCAVE_CHECK_TIMEOUT, cwd=ROOT)
    if p.returncode not in (0, 1):
        raise Stop(f"Concave parity: comparison failed ({p.returncode}); nothing published. "
                   "Repair Concave (DEVELOPMENT.md \"Upstream divergence\": just concave-setup):\n"
                   + p.stderr.decode(errors="replace")[-4000:].strip())
    try:
        verdict = json.loads(p.stdout)
    except ValueError:
        verdict = None
    if (not isinstance(verdict, dict) or verdict.get("format") != 1 or verdict.get("kind") != "concave-parity"
            or verdict.get("baseline") != base or verdict.get("candidate") != new
            or verdict.get("status") != ("passed" if p.returncode == 0 else "failed")
            or not all(isinstance(verdict.get(key), list) for key in ("regressions", "coverage_loss", "discovered_debt"))
            or not isinstance(verdict.get("next"), dict)):
        raise Stop(f"Concave parity: invalid verdict for {new[:12]}; nothing published")
    if p.returncode == 0:
        debt = len(verdict["discovered_debt"])
        print(f"Concave parity passed against {base[:12]} ({time.monotonic() - started:.1f}s)"
              + (f"; {debt} discovered debt finding(s) still unapproved" if debt else ""), file=sys.stderr, flush=True)
        return
    lines = [f"Concave parity refused against {base[:12]}; nothing published."]
    for item in verdict["regressions"][:20]:
        where = f"{item.get('path') or '(repository)'}" + (f":{item['line']}" if item.get("line") else "")
        lines.append(f"  regression {where}: {item.get('rule')}: {item.get('message', '')}")
    for item in verdict["coverage_loss"][:20]:
        lines.append(f"  coverage loss {item.get('path') or '(repository)'}: {item.get('reason')}")
    lines.append("Next: " + str(verdict["next"].get("message", "")))
    if verdict["next"].get("argv"):
        lines.append("  " + " ".join(map(str, verdict["next"]["argv"])))
    raise Stop("\n".join(lines))


def publish(directory: Path, record: dict, check: list[str] | None = None, lock=None,
            check_excludes: list[str] | None = None, check_paths: list[str] | None = None,
            check_inputs: list[str] | None = None) -> str:
    if record.get("abandoned"):
        raise Stop("This capture was abandoned before dispatch; create a new contribution", 4)
    if check is not None:
        record["check"] = check
        # Declared inputs describe the recorded commands; new commands declare their own.
        record.pop("check_inputs", None)
    if check_inputs is not None:
        record["check_inputs"] = check_inputs
    if check_excludes is not None:
        record["check_excludes"] = check_excludes
    if check_paths is not None:
        record["check_paths"] = check_paths
        record.pop("check_excludes", None)
    checked_tree = None
    for _ in range(ATTEMPTS):
        remote = fetch(lock)
        if (record["published"] or record.get("dispatched")) and record.get("candidate") and is_ancestor(record["candidate"], remote):
            record["published"] = True
            save_record(directory, record)
            return record["candidate"]
        try:
            new = candidate(record, remote)
        except Conflict as c:
            c.receipt = str(directory)
            c.directory = save_conflict(c)
            record["conflict"] = str(c.directory)
            save_record(directory, record)
            raise
        record.pop("conflict", None)
        record["candidate"] = new
        record["dispatched"] = False
        save_record(directory, record)
        tree = (git("rev-parse", f"{remote}^{{tree}}"), git("rev-parse", f"{new}^{{tree}}"))
        if tree != checked_tree:
            with unlocked(lock):
                try:
                    check_parity(remote, new)
                except Stop as error:
                    raise Stop(f"{error}\nThe capture stays pending: fix the source, then "
                               f"scripts/land.py --abandon {directory} and land again.", error.code)
                checks = record.get("check")
                # A receipt captured before --check could repeat holds its one command.
                if isinstance(checks, str):
                    checks = [checks]
                excludes = record.get("check_excludes", [])
                declared = {"scratch_key": str(directory.resolve())}
                if record.get("check_inputs"):
                    declared["inputs"] = record["check_inputs"]
                if record.get("check_paths"):
                    check_candidate(new, checks, paths=record["check_paths"], **declared)
                elif excludes:
                    check_candidate(new, checks, excludes, **declared)
                else:
                    check_candidate(new, checks, **declared)
            checked_tree = tree
        if new == remote:
            record["published"] = True
            save_record(directory, record)
            return new
        record["dispatched"] = True
        save_record(directory, record)
        with unlocked(lock):
            p = run(["push", "-q", REMOTE, f"{new}:{BRANCH}"], check=False)
        if not p.returncode:
            record["published"] = True
            save_record(directory, record)
            # The remote may already have advanced again; do not overwrite its tracking ref.
            return new
        # Even an authentication/transport error may follow an accepted push.
        previous_remote = remote
        remote = fetch(lock)
        if is_ancestor(new, remote):
            record["published"] = True
            save_record(directory, record)
            return new
        if not re.search(r"rejected|fetch first|non-fast-forward|stale info", p.stderr.decode(errors="replace")):
            raise Stop(f"Push unconfirmed; frozen receipt retained at {directory}:\n" + p.stderr.decode(errors="replace"))
        # A completed, explicitly rejected push has no pending dispatch.
        record["dispatched"] = False
        save_record(directory, record)
        if remote == previous_remote:
            raise Stop(f"Push rejected without a branch update; frozen receipt retained at {directory}:\n"
                       + p.stderr.decode(errors="replace"))
    raise Stop(f"Main kept moving; resume the frozen capture with --continue {directory}")


def require_no_sync_in_progress() -> None:
    for path in STATE.glob("sync-*/state.json"):
        if json.loads(path.read_text())["phase"] == "applying":
            raise Stop(f"Finish interrupted synchronization first: --sync {path.parent} --writers-paused", 4)


def fingerprint(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def entry_identity(entry: Entry) -> str:
    return f"{entry[0]}:{entry[1]}" if entry else "absent"


def identity_entry(path: str, identity: str) -> Entry:
    """The recorded baseline an identity names; its object must still exist."""
    if identity == "absent":
        return None
    mode, _, oid = identity.partition(":")
    if mode not in ("100644", "100755", "120000") or run(["cat-file", "-e", f"{oid}^{{blob}}"], check=False).returncode:
        raise Stop(f"{path}: --content-base {identity} names no recorded baseline; rebuild from --baseline", 4)
    return (mode, oid)


def receipts_version() -> str:
    return fingerprint(json.dumps([(str(p), r.get("candidate"), r["published"], r.get("abandoned"))
                                   for p, r in records()], sort_keys=True).encode())


def safe_parent(path: str) -> None:
    parent = (ROOT / path).parent
    while parent != ROOT:
        if parent.is_symlink() or (parent.exists() and not parent.is_dir()):
            raise Stop(f"Cannot synchronize through non-directory or symlink parent: {parent}", 4)
        parent = parent.parent


def last_written(path: str) -> float:
    # An absent path's last change is its nearest existing directory's: adding
    # or removing an entry updates that directory's mtime.
    full = ROOT / path
    while True:
        try:
            return os.lstat(full).st_mtime
        except FileNotFoundError:
            full = full.parent


def stale_preimages(working: dict[str, Entry]) -> dict[str, Entry]:
    """Working entries still at the exact pre-image of this checkout's own publication.

    --content and --delete publish bytes from a private file, so the working file
    can keep the bytes that publication replaced while HEAD later advances past
    it. Such a file is no local edit: it equals that capture's baseline and has
    not been written since the capture (seconds-truncated commit time, so ties
    count as written). Its synchronization baseline is that pre-image, never the
    published capture. A file written after the capture may hold a deliberate
    reversion; it keeps the ordinary baseline, as does any other content.
    """
    stale, pending = {}, dict(working)
    for _, record in reversed(records()):
        selected = [p for p in record["paths"] if p in pending] if record["published"] and record.get("base") else []
        if not selected:
            continue
        captured_at = int(git("show", "-s", "--format=%ct", record["capture"]))
        captured, prior = entries(record["capture"], selected), entries(record["base"], selected)
        for path in selected:
            seen = pending[path]
            if captured[path] == seen or last_written(path) >= captured_at:
                del pending[path]
            elif prior[path] == seen:
                stale[path] = pending.pop(path)
            # Otherwise an earlier unreached private publication may own the pre-image.
    return stale


def prepare_sync(remote: str) -> Path:
    require_no_sync_in_progress()
    if any(not r["published"] and not r.get("abandoned") for _, r in records()):
        raise Stop("Finish or abandon pending captures before refreshing the checkout; publication remains available", 4)
    version = receipts_version()
    head = rev("HEAD")
    # Include every tracked local edit in the guarded preview even when its
    # published path has not changed. Preserve it by default, except a working
    # file still at the pre-image of a private publication, which advances.
    dirty = run(["ls-files", "--modified", "--deleted", "-z"]).stdout.decode().split("\0")
    paths = sorted({p for p, _, _ in tree_changes(head, remote)} |
                   set(filter(None, dirty)) |
                   {p for _, r in records() if r["published"] and not is_ancestor(r["candidate"], head)
                    for p in r["paths"]})
    baseline = baselines(head, paths)
    targets = entries(remote, paths)
    working = {p: read_work(p)[0] for p in paths}
    stale = stale_preimages({p: e for p, e in working.items() if e != baseline[p] and (e is None or e[0] != "other")})
    index_before = (GIT_DIR / "index").read_bytes()
    with PrivateIndex() as ix:
        Path(ix.path).write_bytes(index_before)
        if git("write-tree", env=ix.env) != git("rev-parse", f"{head}^{{tree}}"):
            raise Stop("Shared index has staged changes; publication is unaffected, but synchronization must wait", 4)
    directory = STATE / ("sync-" + uuid.uuid4().hex)
    directory.mkdir()
    steps, conflicts = [], []
    with batch_blobs() as read_blob:
        for path in paths:
            target = targets[path]
            safe_parent(path)
            seen, data = read_work(path)
            base = seen if path in stale and stale[path] == seen else baseline[path]
            if seen and seen[0] not in ("100644", "100755", "120000"):
                raise Stop(f"Cannot snapshot non-file {path}", 4)
            desired, output = target, read_blob(target[1]) if target else None
            conflict = False
            if seen != base and seen != target:
                if target == base:
                    desired, output = seen, data
                elif seen and target and (base is None or base[0] != "120000") and seen[0] == target[0] != "120000" and b"\0" not in data and b"\0" not in output:
                    base_data = read_blob(base[1]) if base else b""
                    merged = merge_file(data, base_data, output)
                    if merged is not None:
                        desired, output = (seen[0], oid_of(merged)), merged
                    else:
                        conflict = True
                else:
                    conflict = True
            if conflict:
                conflicts.append(path)
                # Explicit conflict state: even binary/add/delete conflicts need acknowledgment.
                desired, output = seen, data
            for sub, entry, value in (("files", desired, output), ("before", seen, data)):
                if entry:
                    f = directory / sub / path
                    f.parent.mkdir(parents=True, exist_ok=True)
                    f.write_bytes(value)
            steps.append({"path": path, "seen": seen, "mode": (desired or target or seen or ("100644", ""))[0],
                          "incoming": target, "baseline": base})
    # Hash the captured preimages, never re-read live files for recovery.
    # Raw bytes match the prior stdin path even if attributes define filters.
    seen_steps = [step for step in steps if step["seen"]]
    for offset in range(0, len(seen_steps), 128):
        group = seen_steps[offset:offset + 128]
        stored = git("hash-object", "-w", "--no-filters", "--",
                     *[str(directory / "before" / step["path"]) for step in group]).splitlines()
        if stored != [step["seen"][1] for step in group]:
            raise Stop("Captured recovery preimages changed; prepare synchronization again", 4)
    with PrivateIndex() as ix:
        git("read-tree", head, env=ix.env)
        put_entries(ix.env, {s["path"]: s["seen"] for s in steps})
        snapshot = git("commit-tree", git("write-tree", env=ix.env), "-p", head,
                       "-m", "Unfinished checkout recovery snapshot (private)")
    git("update-ref", f"refs/land/sync/{directory.name}", snapshot)
    (directory / "index.before").write_bytes(index_before)
    record = {"kind": "sync", "head": head, "target": remote, "phase": "prepared",
              "receipts_version": version,
              "steps": steps, "conflicts": conflicts, "snapshot": snapshot,
              "index_before": fingerprint((directory / "index.before").read_bytes())}
    atomic_json(directory / "state.json", record)
    if receipts_version() != version:
        raise Stop(f"Publication changed during synchronization preparation; prepare again (old preview: {directory})", 4)
    return directory


# Unreferenced conflict files and unappliable previews stay this long, for a
# reader still comparing them; recovery itself lives in refs/land.
PRUNE_AFTER = 24 * 3600


def prune_state() -> None:
    """Remove private directories no recovery needs, under the exclusive
    workspace lock. Every refs/land snapshot stays: the bytes a synchronization
    saw and wrote are Git objects behind it. A finished synchronization's files
    go at once; a prepared one HEAD has moved past, and a conflict no pending
    capture references, after PRUNE_AFTER. Applying journals, pending captures
    and their conflicts are never touched."""
    head = rev("HEAD")
    cutoff = time.time() - PRUNE_AFTER
    referenced = {str(Path(r["conflict"]).resolve()) for _, r in records()
                  if r.get("conflict") and not r["published"] and not r.get("abandoned")}
    for path in STATE.glob("sync-*/state.json"):
        record = json.loads(path.read_text())
        finished = record["phase"] == "done"
        unappliable = record["phase"] == "prepared" and head not in (record["head"], record["target"]) and path.stat().st_mtime < cutoff
        if finished or unappliable:
            shutil.rmtree(path.parent)
    for directory in STATE.glob("conflict-*"):
        if str(directory.resolve()) not in referenced and directory.stat().st_mtime < cutoff:
            shutil.rmtree(directory)


def sync_checkout(directory: Path, paused: bool, resolved_paths: list[str]) -> str:
    if not paused:
        raise Stop("Synchronization requires --writers-paused: pause source writers or ensure all use --edit", 4)
    record = json.loads((directory / "state.json").read_text())
    if record.get("kind") != "sync":
        raise Stop("Not a checkout synchronization directory")
    if record["phase"] == "done":
        return record["target"]
    with locked("workspace.lock"):
        index_lock = GIT_DIR / "index.lock"
        try:
            fd = os.open(index_lock, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        except FileExistsError:
            raise Stop("Git index is in use; retry synchronization", 4)
        try:
            os.close(fd)
            head, target = record["head"], record["target"]
            if rev("HEAD") not in (head, target):
                raise Stop("HEAD changed; prepare a fresh synchronization", 4)
            if record["phase"] == "prepared":
                if receipts_version() != record["receipts_version"]:
                    raise Stop("Publication changed since preparation; prepare a new synchronization", 4)
                unresolved = set(record["conflicts"]) - set(resolved_paths)
                if unresolved:
                    raise Stop("Resolve private files and acknowledge each with --resolved PATH: " + ", ".join(sorted(unresolved)), 4)
                with PrivateIndex() as ix:
                    git("read-tree", target, env=ix.env)
                    values = {}
                    for step in record["steps"]:
                        f = directory / "files" / step["path"]
                        entry = None
                        if f.exists():
                            entry = (step["mode"], git("hash-object", "-w", "--", str(f)))
                        step["after"] = entry
                        values[step["path"]] = entry
                    put_entries(ix.env, values)
                    marker_check(ix.env)
                    recovery = git("commit-tree", git("write-tree", env=ix.env), "-p", record["snapshot"],
                                   "-m", "Resolved checkout synchronization (private)")
                    git("update-ref", f"refs/land/sync/{directory.name}", recovery)
                    # Shared index contains only committed target, never unfinished edits.
                    git("read-tree", target, env=ix.env)
                    (directory / "index.after").write_bytes(Path(ix.path).read_bytes())
                record["index_after"] = fingerprint((directory / "index.after").read_bytes())
            for step in record["steps"]:
                safe_parent(step["path"])
                allowed = [tuple(step["seen"]) if step["seen"] else None]
                if record["phase"] == "applying":
                    allowed.append(tuple(step["after"]) if step["after"] else None)
                if read_work(step["path"])[0] not in allowed:
                    raise Stop(f"{step['path']} changed since preparation; no further synchronization writes made", 4)
            index_hash = fingerprint((GIT_DIR / "index").read_bytes())
            allowed_indexes = [record["index_before"]]
            if record["phase"] == "applying":
                allowed_indexes.append(record["index_after"])
            if index_hash not in allowed_indexes:
                raise Stop("Index changed since preparation; no synchronization writes made", 4)
            record["phase"] = "applying"
            atomic_json(directory / "state.json", record)
            for step in record["steps"]:
                after = tuple(step["after"]) if step["after"] else None
                if read_work(step["path"])[0] != after:
                    write_file(step["path"], after, blob(after[1]) if after else None)
            with open(index_lock, "wb") as f:
                f.write((directory / "index.after").read_bytes())
                f.flush()
                os.fsync(f.fileno())
            os.replace(index_lock, GIT_DIR / "index")
            if rev("HEAD") != target:
                git("update-ref", "-m", "land: synchronize published changes", BRANCH, target, head)
            record["phase"] = "done"
            atomic_json(directory / "state.json", record)
            prune_state()
            return target
        finally:
            if index_lock.exists():
                index_lock.unlink()


def main() -> int:
    if len(sys.argv) > 1 and sys.argv[1] == "--edit":
        command = sys.argv[2:]
        if command[:1] == ["--"]:
            command = command[1:]
        if not command:
            raise Stop("--edit requires a command")
        with locked("workspace.lock", exclusive=False):
            require_no_sync_in_progress()
            return subprocess.run(command, cwd=ROOT).returncode
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("-m", "--message", action="append", default=[],
                    help="commit message; repeat for further paragraphs, as with git commit -m")
    ap.add_argument("--continue", dest="resume", metavar="DIR")
    ap.add_argument("--merge-commit", metavar="COMMIT", help="publish a prepared ordinary upstream merge, preserving both parents")
    ap.add_argument("--abandon", metavar="DIR", help="abandon a failed capture which was never dispatched")
    ap.add_argument("--check", action="append", metavar="COMMAND",
                    help="check the merged candidate in a temporary checkout before pushing; "
                         "repeat to run several commands in order, stopping at the first failure")
    ap.add_argument("--prepare", action="store_true", help="print a nonpublishing candidate as JSON; creates no pending capture")
    ap.add_argument("--check-candidate", metavar="COMMIT", help="check a prepared commit without publishing or capturing")
    ap.add_argument("--cleanup-check", metavar="DIRECTORY", help="after owned processes stop, remove an unchanged interrupted check bound by --check-candidate")
    ap.add_argument("--check-exclude", action="append", metavar="DIRECTORY",
                    help="omit this repository-relative directory from the disposable check checkout; repeat as needed")
    ap.add_argument("--check-path", action="append", metavar="PATH",
                    help="materialize only these literal files or directories in the disposable checkout")
    ap.add_argument("--check-input", action="append", metavar="PATH",
                    help="declare a repository path the checks read; a candidate whose declared entries and "
                         "commands equal an already passed candidate's reuses that pass; repeat as needed")
    ap.add_argument("--publication-status", metavar="ID", help="read versioned publication JSON without fetching or changing state")
    ap.add_argument("--publication-id", help="bind a caller-owned 64-hex identity to one durable capture")
    ap.add_argument("--baseline", metavar="PATH", help="print the recorded local baseline for preparing --content")
    ap.add_argument("--baselines", nargs="+", metavar="PATH", help="print bounded baseline identities and base64 bytes as one JSON snapshot")
    ap.add_argument("--published", action="store_true",
                    help="with --baseline or --baselines, inspect freshly fetched origin/main instead of local receipt state")
    ap.add_argument("--sync", metavar="DIR", help="apply a previously prepared checkout synchronization")
    ap.add_argument("--writers-paused", action="store_true")
    ap.add_argument("--resolved", action="append", default=[], metavar="PATH", help="acknowledge a privately resolved synchronization conflict")
    ap.add_argument("--content", action="append", default=[], metavar="PATH=FILE",
                    help="land named PATH with FILE's bytes (recorded baseline plus only your hunks)")
    ap.add_argument("--delete", action="append", default=[], metavar="PATH",
                    help="delete a reviewed path, bound by --content-base; preserves working files")
    ap.add_argument("--content-mode", action="append", default=[], metavar="PATH=MODE",
                    help="bind --content to Git mode 100644 or 100755")
    ap.add_argument("--content-base", action="append", default=[], metavar="PATH=IDENTITY",
                    help="bind each --content file to the identity printed by --baseline")
    ap.add_argument("--content-sha256", action="append", default=[], metavar="PATH=SHA256",
                    help="require these exact reviewed --content bytes during capture")
    ap.add_argument("paths", nargs="*")
    a = ap.parse_args()
    if a.cleanup_check:
        if not a.check_candidate or len(sys.argv) != 5:
            ap.error("--cleanup-check requires only --check-candidate COMMIT")
        cleanup_check(a.cleanup_check, a.check_candidate)
        return 0
    if a.publication_status:
        if not re.fullmatch(r"[a-f0-9]{64}", a.publication_status) or len(sys.argv) != 3:
            ap.error("--publication-status requires only one 64-hex identity")
        print(json.dumps(publication_status(a.publication_status), sort_keys=True))
        return 0
    a.message = "\n\n".join(m.strip() for m in a.message if m.strip())
    if a.prepare and (not a.paths or a.check or a.message):
        ap.error("--prepare requires paths, without --check or -m")
    if a.check_candidate and not a.check:
        ap.error("--check-candidate requires --check")
    if a.published and not (a.baseline or a.baselines):
        ap.error("--published requires --baseline or --baselines")
    if a.prepare:
        a.message = "Contribution preview (private)"
    if bool(a.paths) != bool(a.message):
        ap.error("give both -m and the paths you changed, or neither")
    if sum(bool(v) for v in (a.paths, a.resume, a.baseline, a.baselines, a.sync, a.abandon, a.merge_commit, a.check_candidate)) > 1:
        ap.error("choose one of paths, --continue, --baseline, --abandon, --sync or --merge-commit")
    if a.check_exclude is not None:
        if not (a.paths or a.resume or a.merge_commit or a.check_candidate) or (not a.check and not a.resume):
            ap.error("--check-exclude requires --check with paths, --merge-commit or --continue")
        for directory in a.check_exclude:
            if (not re.fullmatch(r"[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*", directory)
                    or any(part in (".", "..", ".git") for part in directory.split("/"))):
                ap.error("--check-exclude must name a repository-relative directory without patterns")
    if a.publication_id and (not re.fullmatch(r"[a-f0-9]{64}", a.publication_id) or not (a.paths or a.resume) or a.prepare):
        ap.error("--publication-id requires a 64-hex identity and publication paths or --continue")
    if a.check_path is not None:
        if a.check_exclude or not (a.paths or a.resume or a.check_candidate) or (not a.check and not a.resume):
            ap.error("--check-path requires a candidate check and cannot combine with --check-exclude")
        if len(a.check_path) > 20000:
            ap.error("--check-path exceeds 20000 paths")
        for path in a.check_path:
            if (not re.fullmatch(r"[A-Za-z0-9_.@+-]+(?:/[A-Za-z0-9_.@+-]+)*/?", path)
                    or any(part in (".", "..", ".git") for part in path.rstrip("/").split("/"))):
                ap.error("--check-path must name a literal repository-relative file or directory")
    if a.check_input is not None:
        if not (a.paths or a.resume or a.merge_commit or a.check_candidate) or (not a.check and not a.resume):
            ap.error("--check-input requires --check with paths, --merge-commit, --check-candidate or --continue")
        if len(a.check_input) > MAX_CHECK_INPUTS:
            ap.error(f"--check-input exceeds {MAX_CHECK_INPUTS} paths")
        for path in a.check_input:
            if (not re.fullmatch(r"[A-Za-z0-9_.@+-]+(?:/[A-Za-z0-9_.@+-]+)*/?", path)
                    or any(part in (".", "..", ".git") for part in path.rstrip("/").split("/"))):
                ap.error("--check-input must name a literal repository-relative file or directory")
    cwd = Path.cwd()
    paths = [os.path.relpath((cwd / p).absolute(), ROOT) for p in a.paths]
    for p in paths:
        if p == ".." or p.startswith("../") or p == ".git" or p.startswith(".git/"):
            ap.error(f"{p} is outside the repository")
    content = {}
    for spec in a.content:
        path, sep, source = spec.partition("=")
        path = os.path.relpath((cwd / path).absolute(), ROOT)
        if not sep or path not in paths or path in content or not Path(source).is_file():
            ap.error(f"--content {spec}: name one of the landed paths and an existing file")
        content[path] = Path(source).absolute()
    for value in a.delete:
        path = os.path.relpath((cwd / value).absolute(), ROOT)
        if path not in paths or path in content:
            ap.error("--delete must name one unique landed path without --content")
        content[path] = None
    content_modes = {}
    for spec in a.content_mode:
        path, sep, mode = spec.partition("=")
        path = os.path.relpath((cwd / path).absolute(), ROOT)
        if not sep or content.get(path) is None or path in content_modes or mode not in ("100644", "100755"):
            ap.error("--content-mode requires a unique --content path and mode 100644 or 100755")
        content_modes[path] = mode
    content_bases = {}
    for spec in a.content_base:
        path, sep, identity = spec.partition("=")
        path = os.path.relpath((cwd / path).absolute(), ROOT)
        if not sep or path not in content or path in content_bases:
            ap.error("--content-base must name a unique --content or --delete path")
        content_bases[path] = identity
    if set(content_bases) != set(content):
        ap.error("each --content or --delete needs --content-base PATH=IDENTITY from --baseline")
    content_sha256 = {}
    for spec in a.content_sha256:
        path, sep, digest = spec.partition("=")
        path = os.path.relpath((cwd / path).absolute(), ROOT)
        if not sep or content.get(path) is None or path in content_sha256 or not re.fullmatch(r"[a-f0-9]{64}", digest):
            ap.error("--content-sha256 needs a unique --content path and a SHA256 digest")
        content_sha256[path] = digest
    try:
        with locked("lock") as lock:
            check_quiet_checkout()
            # Local baselines retain receipt state without fetching. Published
            # inspection fetches one exact remote basis; capture still merges
            # only the reviewed delta and preserves working files and the index.
            remote = None if (a.baseline or a.baselines) and not a.published else fetch(lock)
            if a.check_candidate:
                commit = rev(a.check_candidate)
                with unlocked(lock):
                    check_candidate(commit, a.check, a.check_exclude, a.check_path, a.check_input)
                return 0
            if a.prepare:
                item = capture(paths, a.message, content, None, content_bases, persist=False, content_sha256=content_sha256, content_modes=content_modes)
                if item is None:
                    raise Stop("No contribution differs from its published baseline")
                _, record = item
                try:
                    commit = candidate(record, remote)
                except Conflict as conflict:
                    raise Stop("Candidate conflicts with origin/main in: " + ", ".join(conflict.paths) +
                               "; synchronize and recapture", 3)
                # A preview applies the publication rule, so it cannot promise a refused landing.
                with unlocked(lock):
                    check_parity(remote, commit)
                print(json.dumps({"format": 1, "kind": "land-candidate", "commit": commit,
                                  "tree": git("rev-parse", commit + "^{tree}"), "parent": remote,
                                  "paths": record["paths"]}, sort_keys=True))
                return 0
            if a.abandon:
                directory = Path(a.abandon).resolve()
                with publication_guard(directory):
                    record = json.loads((directory / "state.json").read_text())
                    if record.get("kind") != "publication" or record["published"] or record.get("dispatched"):
                        raise Stop("Only a capture never dispatched may be abandoned; reconcile a dispatched push with --continue", 4)
                    record["abandoned"] = True
                    save_record(directory, record)
                print("abandoned: source edits preserved; a new capture may be made")
                return 0
            if a.baseline:
                path = os.path.relpath((cwd / a.baseline).absolute(), ROOT)
                entry = (entries(remote, [path]) if a.published else baselines(rev("HEAD"), [path]))[path]
                if entry:
                    sys.stdout.buffer.write(blob(entry[1]))
                print("baseline-id: " + entry_identity(entry), file=sys.stderr)
                return 0
            if a.baselines:
                selected = sorted(set(os.path.relpath((cwd / path).absolute(), ROOT) for path in a.baselines))
                if len(selected) > 20000 or any(path == ".." or path.startswith("../") or path == ".git" or path.startswith(".git/") for path in selected):
                    raise Stop("Baselines require at most 20000 repository-relative files")
                head = remote if a.published else rev("HEAD")
                values = entries(head, selected) if a.published else baselines(head, selected)
                files, size = {}, 0
                for path, entry in values.items():
                    data = blob(entry[1]) if entry else b""
                    size += len(data)
                    if len(data) > 4 * 1024 * 1024 or size > 64 * 1024 * 1024:
                        raise Stop("Baseline source bytes exceed bound")
                    files[path] = {"identity": entry_identity(entry), "data": base64.b64encode(data).decode()}
                print(json.dumps({"format": 1, "kind": "land-baselines", "head": head, "files": files}, sort_keys=True))
                return 0
            if a.sync:
                result = sync_checkout(Path(a.sync).resolve(), a.writers_paused, a.resolved)
                print(f"synchronized: local main at {result[:12]}")
                return 0
            if not paths and not a.resume and not a.merge_commit:
                with unlocked(lock):
                    with locked("workspace.lock", exclusive=False):
                        directory = prepare_sync(remote)
                state = json.loads((directory / "state.json").read_text())
                print(f"Synchronization prepared: {directory}")
                for path in state["conflicts"]:
                    print(f"conflict: {path}; compare before/, files/, and git show {remote[:12]}:{path}")
                print(f"After resolving conflicts and coordinating writers: scripts/land.py --sync {directory} --writers-paused" +
                      "".join(f" --resolved {p}" for p in state["conflicts"]))
                return 0
            if a.merge_commit:
                directory, record = capture_upstream_merge(a.merge_commit, remote, a.check)
            elif a.resume:
                resume = Path(a.resume).resolve()
                state = json.loads((resume / "state.json").read_text())
                if a.publication_id:
                    owner = resume if state.get("kind") == "publication" else Path(state.get("receipt", ""))
                    if owner.resolve() != (STATE / "receipts" / a.publication_id).resolve():
                        raise Stop("Resolution belongs to another publication", 4)
                if state.get("kind") == "publication":
                    directory, record = resume, state
                else:
                    resolved = resolved_merge(resume)
                    if state.get("receipt"):
                        directory = Path(state["receipt"])
                        record = json.loads((directory / "state.json").read_text())
                    else:
                        # Adopt an already prepared merge without recapturing any live files.
                        base = rev(state["ours"] + "^")
                        selected = [p for p, _, _ in tree_changes(base, state["ours"])]
                        if baselines(rev("HEAD"), selected) != entries(base, selected):
                            raise Stop("This prepared merge predates other local publications on its paths; recapture your hunks using --baseline, keeping the private resolution for reference", 4)
                        directory = STATE / "receipts" / uuid.uuid4().hex
                        record = {"kind": "publication", "sequence": max((r["sequence"] for _, r in records()), default=0) + 1,
                                  "checkout_head": rev("HEAD"), "base": base, "capture": state["ours"],
                                  "paths": selected, "resolved": resolved,
                                  "message": git("log", "-1", "--format=%B", state["ours"]),
                                  "check": a.check, "published": False}
                        save_record(directory, record)
                        state["receipt"] = str(directory)
                        atomic_json(resume / "state.json", state)
                    record["resolved"] = resolved
            else:
                item = capture(paths, a.message, content, a.check, content_bases, content_sha256=content_sha256, content_modes=content_modes, publication_id=a.publication_id)
                if item is None:
                    print("landed: selected paths already match their published baseline; shared checkout unchanged")
                    return 0
                directory, record = item
            with publication_guard(directory):
                result = publish(directory, record, a.check, lock, a.check_exclude, a.check_path, a.check_input)
            print(f"landed: published {result[:12]} to origin/main; shared checkout unchanged")
            print(f"receipt: {directory}")
            if rev("HEAD") != result:
                print("note: local synchronization pending; run scripts/land.py to prepare it")
    except Conflict as c:
        d = getattr(c, "directory", None) or save_conflict(c)
        print("Not published; shared checkout unchanged. Merging origin/main conflicts in:\n  " + "\n  ".join(c.paths) +
              f"\nEach file with conflict markers is under {d}/files/. Edit them there (delete a file there to delete it), "
              f"then run: scripts/land.py --continue {d}\n"
              f"Sides: git show {c.ours[:12]}:<path> (yours), git show {c.theirs[:12]}:<path> (origin).", file=sys.stderr)
        return 3
    except Stop as s:
        print(s, file=sys.stderr)
        return s.code
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Stop as error:
        print(error, file=sys.stderr)
        sys.exit(error.code)
