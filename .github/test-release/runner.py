#!/usr/bin/env python3
"""Runs .github/workflows/release.yml locally, step by step, like GitHub does.

- Every `run:` step goes to `bash -e <file>` (GitHub's default shell on
  Linux), with the workflow, job and step env, GITHUB_OUTPUT and the
  ${{ }} expressions the workflow uses.
- `if:` is evaluated (inputs.*, success/failure/cancelled, steps.*.outputs.*).
- `uses:` steps: checkout = clone of the fake origin at GITHUB_SHA (depth 1
  without tags, or the whole history with fetch-depth 0); setup-go checks
  the local Go matches go.mod; setup-node checks node's major version;
  upload/download-artifact copy files (dropping the executable bit, as the
  zip does).
- Unknown expressions or actions fail loudly instead of being skipped.

usage: runner.py WORKDIR VERSION RELEASE(true|false)
  WORKDIR holds origin.git and gh-state.json (shared between runs).
The gh stand-in next to this file is run with python3 (via a wrapper on
PATH), so it needs no executable bit either.
"""
import json, os, re, shutil, subprocess, sys, tempfile
import yaml

workdir, version, release = sys.argv[1], sys.argv[2], sys.argv[3] == "true"
origin = os.path.join(workdir, "origin.git")
here = os.path.dirname(os.path.abspath(__file__))
sha = subprocess.check_output(["git", "-C", origin, "rev-parse", "master"], text=True).strip()
repo = "me/fdsync"

src = subprocess.check_output(["git", "-C", origin, "show", f"{sha}:.github/workflows/release.yml"], text=True)
wf = yaml.safe_load(src)
inputs_def = wf[True]["workflow_dispatch"]["inputs"]
assert set(inputs_def) == {"version", "release"}, inputs_def
inputs = {"version": version, "release": release}

def expr_value(e, ctx):
    e = e.strip()
    m = re.fullmatch(r"inputs\.(\w+)", e)
    if m: return ctx["inputs"][m.group(1)]
    if e == "github.token": return "fake-token"
    m = re.fullmatch(r"steps\.([\w-]+)\.outputs\.([\w-]+)", e)
    if m: return ctx["steps"].get(m.group(1), {}).get(m.group(2), "")
    raise SystemExit(f"runner: unsupported expression {e!r}")

def subst(s, ctx):
    if not isinstance(s, str): return s
    def rep(m):
        v = expr_value(m.group(1), ctx)
        return ("true" if v else "false") if isinstance(v, bool) else str(v)
    return re.sub(r"\$\{\{(.*?)\}\}", rep, s)

def cond(c, ctx):
    if c is None: return not ctx["failed"]
    if isinstance(c, bool): return c
    c = re.sub(r"^\$\{\{(.*)\}\}$", r"\1", c.strip())
    status_fn = re.search(r"\b(success|failure|cancelled|always)\(\)", c)
    py = c
    py = re.sub(r"\bfailure\(\)", repr(ctx["failed"]), py)
    py = re.sub(r"\bsuccess\(\)", repr(not ctx["failed"]), py)
    py = re.sub(r"\bcancelled\(\)", "False", py)
    py = re.sub(r"\balways\(\)", "True", py)
    py = re.sub(r"(inputs\.\w+|steps\.[\w-]+\.outputs\.[\w-]+)", lambda m: repr(expr_value(m.group(1), ctx)), py)
    py = py.replace("&&", " and ").replace("||", " or ").replace("!", " not ").replace(" not =", "!=")
    if re.search(r"[A-Za-z_]\w*\.", py): raise SystemExit(f"runner: unsupported if {c!r}")
    v = eval(py, {"__builtins__": {}}, {})
    # Without a status function, GitHub adds success().
    return bool(v) if status_fn else (bool(v) and not ctx["failed"])

artifacts = os.path.join(workdir, "artifacts")
bindir = os.path.join(workdir, "bin")
os.makedirs(bindir, exist_ok=True)
with open(os.path.join(bindir, "gh"), "w") as f:
    f.write(f'#!/bin/sh\nexec python3 "{os.path.join(here, "gh")}" "$@"\n')
os.chmod(os.path.join(bindir, "gh"), 0o755)
tools = os.path.join(workdir, "tools")  # npm --global goes here, not into the real system
os.makedirs(tools, exist_ok=True)

def run_job(name, job, needs_ok):
    print(f"\n======== job {name}")
    ctx = {"inputs": inputs, "steps": {}, "failed": False}
    if job.get("needs") and not needs_ok:
        print("  (skipped: needs failed)"); return None
    if not cond(job.get("if"), ctx):
        print("  (skipped by if)"); return None
    perms = job.get("permissions", wf.get("permissions", {}))
    ws = tempfile.mkdtemp(prefix=f"ws-{name}-", dir=workdir)
    for i, step in enumerate(job["steps"]):
        label = step.get("name") or step.get("uses")
        if not cond(step.get("if"), ctx):
            print(f"  --- skip: {label}"); continue
        print(f"  --- step: {label}")
        env = dict(os.environ)
        env.update({k: subst(v, ctx) for k, v in (wf.get("env") or {}).items()})
        env.update({k: subst(v, ctx) for k, v in (job.get("env") or {}).items()})
        env.update({k: subst(v, ctx) for k, v in (step.get("env") or {}).items()})
        out_file = os.path.join(ws, f".out-{i}")
        open(out_file, "w").close()
        env.update({
            "GITHUB_SHA": sha, "GITHUB_REPOSITORY": repo, "GITHUB_SERVER_URL": "https://github.com",
            "GITHUB_OUTPUT": out_file, "GITHUB_WORKSPACE": ws, "CI": "true",
            "PATH": f"{bindir}:{tools}/bin:" + os.environ["PATH"],
            "npm_config_prefix": tools,
            "FAKE_GH_STATE": os.path.join(workdir, "gh-state.json"), "FAKE_GH_ORIGIN": origin,
        })
        if "GH_TOKEN" not in (step.get("env") or {}): env.pop("GH_TOKEN", None)
        ok = True
        if "uses" in step:
            ok = do_action(step["uses"], {k: subst(v, ctx) for k, v in (step.get("with") or {}).items()}, ws, perms)
        else:
            script = os.path.join(ws, f".step-{i}.sh")
            open(script, "w").write(subst(step["run"], ctx))
            cwd = os.path.join(ws, step.get("working-directory", "."))
            ok = subprocess.run(["bash", "-e", script], cwd=cwd, env=env).returncode == 0
        outs = {}
        for line in open(out_file):
            if "=" in line:
                k, v = line.rstrip("\n").split("=", 1); outs[k] = v
        if "id" in step: ctx["steps"][step["id"]] = outs
        if not ok:
            print(f"  !!! step failed: {label}")
            ctx["failed"] = True
    print(f"  job {name}: {'FAILED' if ctx['failed'] else 'ok'}")
    return not ctx["failed"]

def git(*a, cwd=None):
    return subprocess.run(["git", *a], cwd=cwd, capture_output=True, text=True)

def do_action(uses, w, ws, perms):
    known = {"version", "release"}
    if uses == "actions/checkout@v4":
        assert set(w) <= {"fetch-depth"}, w
        depth = int(w.get("fetch-depth", 1))
        if depth == 0:
            r = git("clone", "-q", "file://" + origin, ws + "/repo")
        else:  # depth 1, no tags (like actions/checkout)
            r = git("clone", "-q", "--depth", str(depth), "--no-tags", "file://" + origin, ws + "/repo")
        if r.returncode: print(r.stderr); return False
        for f in os.listdir(ws + "/repo"): shutil.move(ws + "/repo/" + f, ws)
        os.rmdir(ws + "/repo")
        git("checkout", "-q", "--detach", sha, cwd=ws)
        git("config", "remote.origin.pushurl", "file://" + origin + (".readonly" if perms.get("contents") != "write" else ""), cwd=ws)
        return True
    if uses == "actions/setup-go@v5":
        assert set(w) <= {"go-version-file", "cache-dependency-path"}, w
        want = re.search(r"^go (\S+)", open(os.path.join(ws, w["go-version-file"])).read(), re.M).group(1)
        have = subprocess.check_output(["go", "env", "GOVERSION"], text=True).strip()
        assert os.path.exists(os.path.join(ws, w["cache-dependency-path"]))
        print(f"    go.mod wants {want}, runner has {have}")
        return have == "go" + want
    if uses == "actions/setup-node@v4":
        assert set(w) <= {"node-version"}, w
        have = subprocess.check_output(["node", "--version"], text=True).strip()
        print(f"    node {w['node-version']} wanted, {have} here")
        return have.startswith(f"v{w['node-version']}.")
    if uses == "actions/upload-artifact@v4":
        assert set(w) <= {"name", "path", "if-no-files-found"}, w
        import glob
        files = glob.glob(os.path.join(ws, w["path"]))
        if not files: print("    no files"); return False
        dst = os.path.join(artifacts, w["name"])
        if os.path.exists(dst): print(f"    artifact {w['name']} exists in this run"); return False
        os.makedirs(dst)
        for f in files:
            shutil.copyfile(f, os.path.join(dst, os.path.basename(f)))  # mode dropped, like the zip
        print(f"    uploaded {len(files)} files")
        return True
    if uses == "actions/download-artifact@v4":
        assert set(w) <= {"name", "path"}, w
        src = os.path.join(artifacts, w["name"])
        if not os.path.isdir(src): print("    no such artifact"); return False
        dst = os.path.join(ws, w["path"]); os.makedirs(dst, exist_ok=True)
        for f in os.listdir(src): shutil.copyfile(os.path.join(src, f), os.path.join(dst, f))
        return True
    raise SystemExit(f"runner: unsupported action {uses}")

shutil.rmtree(artifacts, ignore_errors=True)
print(f"run: version={version!r} release={release} sha={sha[:7]}")
results = {}
for name, job in wf["jobs"].items():
    needs = job.get("needs")
    needs = [needs] if isinstance(needs, str) else (needs or [])
    results[name] = run_job(name, job, all(results.get(n) for n in needs))
print("\nRESULT", json.dumps(results))
sys.exit(0 if all(v is not False for v in results.values()) else 1)
