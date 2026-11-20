#!/usr/bin/env bash
# Runs .github/workflows/release.yml locally through every release path, one
# after another on a fake GitHub (a bare repo + a stand-in gh), and checks
# the end state of each. Uses the working tree as it is (uncommitted
# changes included), with every executable bit stripped.
#
#   bash .github/test-release/scenarios.sh      (needs go, node, npm, python3 + PyYAML)
#
# Builds for real (panel + 5 executables) on each run: takes a few minutes.
set -uo pipefail
H=$(dirname "$(readlink -f "$0")")
W=$(mktemp -d "${TMPDIR:-/tmp}/fdsync-release-test.XXXXXX")
trap 'chmod -R u+rwx "$W" 2>/dev/null; rm -rf "$W"' EXIT
export PATH=$HOME/.local/go/bin:$PATH
fails=0
check() { if eval "$2"; then echo "PASS  $1"; else echo "FAIL  $1   [$2]"; fails=$((fails+1)); fi; }
run() { python3 "$H/runner.py" "$W" "$1" "$2" > "$W/last.log" 2>&1; echo $?; }
tagc() { git -C "$W/origin.git" rev-parse -q --verify "refs/tags/$1^{commit}" 2>/dev/null; }
head_() { git -C "$W/origin.git" rev-parse master; }
rel() { python3 -c "import json,sys;s=json.load(open('$W/gh-state.json'));r=[x for x in s['releases'] if x['tag']=='$1'];print(json.dumps(r[0]) if r else '')"; }
relf() { python3 -c "import json,sys;r=json.loads(sys.argv[1]);print(r$2)" "$(rel "$1")"; }
nrel() { python3 -c "import json;print(len(json.load(open('$W/gh-state.json'))['releases']))" 2>/dev/null || echo 0; }
step_failed() { grep -q "!!! step failed: $1" "$W/last.log"; }

bash "$H/setup.sh" "$W" >/dev/null

echo "== invalid versions"
for v in v1.0.0 1.0 01.0.0 "1.0.0 " "1.0.0;id"; do
  rc=$(run "$v" true); check "rejects '$v'" "[ $rc = 1 ] && step_failed 'Check the version' && [ \$(nrel) = 0 ]"
done

echo "== release 1.0.0 (first release)"
rc=$(run 1.0.0 true)
check "run succeeds" "[ $rc = 0 ]"
check "tag v1.0.0 at the built commit" "[ \"\$(tagc v1.0.0)\" = \"\$(head_)\" ]"
check "tag is annotated" "[ \"\$(git -C $W/origin.git cat-file -t v1.0.0)\" = tag ]"
check "release published, not draft" "[ \"\$(relf v1.0.0 \"['draft']\")\" = False ]"
check "marked latest" "[ \"\$(relf v1.0.0 \"['latest']\")\" = true ]"
check "6 assets" "[ \"\$(relf v1.0.0 \"['assets'].__len__()\")\" = 6 ]"
check "notes: since the first commit" "relf v1.0.0 \"['notes']\" | grep -q 'Changes since the first commit'"
check "notes: compare link from first commit" "relf v1.0.0 \"['notes']\" | grep -q \"compare/\$(git -C $W/origin.git rev-list --max-parents=0 master)...v1.0.0\""
check "notes: install line uses the repo" "relf v1.0.0 \"['notes']\" | grep -q 'raw.githubusercontent.com/me/fdsync/HEAD/install.sh'"
check "SHA256SUMS matches the files" "(cd $W/artifacts/fdsync-1.0.0 && sha256sum -c --quiet SHA256SUMS)"
check "binaries carry the version" "chmod +x $W/artifacts/fdsync-1.0.0/fdsync-linux-amd64 && [ \"\$($W/artifacts/fdsync-1.0.0/fdsync-linux-amd64 version)\" = 'fdsync 1.0.0' ]"

echo "== 1.0.0 again"
rc=$(run 1.0.0 true)
check "fails before building" "[ $rc = 1 ] && step_failed 'Check the release and tag' && ! grep -q 'step: Build the panel' $W/last.log"
check "still one release" "[ \$(nrel) = 1 ]"

echo "== 1.0.0 build-only again (allowed)"
rc=$(run 1.0.0 false); check "build-only of a released version runs" "[ $rc = 0 ]"

echo "== two commits, release 1.1.0"
bash "$H/commit.sh" "$W" "feature A"; bash "$H/commit.sh" "$W" "feature B"
rc=$(run 1.1.0 true)
check "run succeeds" "[ $rc = 0 ]"
check "tag at new head" "[ \"\$(tagc v1.1.0)\" = \"\$(head_)\" ]"
check "notes: since v1.0.0" "relf v1.1.0 \"['notes']\" | grep -q 'Changes since v1.0.0'"
check "notes: lists exactly the 2 new commits" "[ \"\$(relf v1.1.0 \"['notes']\" | grep -c '^- feature')\" = 2 ] && ! relf v1.1.0 \"['notes']\" | grep -q 'initial import'"
check "notes: compare v1.0.0...v1.1.0" "relf v1.1.0 \"['notes']\" | grep -q 'compare/v1.0.0...v1.1.0'"
check "latest" "[ \"\$(relf v1.1.0 \"['latest']\")\" = true ]"

echo "== older-line fix 1.0.1 after 1.1.0"
bash "$H/commit.sh" "$W" "fix C"
rc=$(run 1.0.1 true)
check "run succeeds" "[ $rc = 0 ]"
check "not latest" "[ \"\$(relf v1.0.1 \"['latest']\")\" = false ]"
check "notes: since v1.0.0" "relf v1.0.1 \"['notes']\" | grep -q 'compare/v1.0.0...v1.0.1'"

echo "== tag v2.0.0 already on another commit"
git -C "$W/origin.git" tag v2.0.0 "$(tagc v1.0.0)"
rc=$(run 2.0.0 true)
check "fails before building" "[ $rc = 1 ] && step_failed 'Check the release and tag' && ! grep -q 'step: Build the panel' $W/last.log"
check "foreign tag untouched" "[ \"\$(tagc v2.0.0)\" = \"\$(tagc v1.0.0)\" ]"
git -C "$W/origin.git" tag -d v2.0.0 >/dev/null

echo "== tag v2.1.0 already at the built commit (left by an earlier run)"
git -C "$W/origin.git" tag v2.1.0 "$(head_)"
rc=$(run 2.1.0 true)
check "run succeeds, tag reused" "[ $rc = 0 ] && grep -q 'already exists at .*: using it' $W/last.log"
check "release published" "[ \"\$(relf v2.1.0 \"['draft']\")\" = False ]"

echo "== upload fails during 2.2.0"
bash "$H/commit.sh" "$W" "feature D"
FAKE_GH_FAIL_UPLOAD=fdsync-linux-arm64 rc=$(FAKE_GH_FAIL_UPLOAD=fdsync-linux-arm64 run 2.2.0 true)
check "run fails at publish" "[ $rc = 1 ] && step_failed 'Publish the release'"
check "undo ran" "grep -q 'step: Undo the tag after a failure' $W/last.log && ! step_failed 'Undo the tag after a failure'"
check "draft deleted" "[ -z \"\$(rel v2.2.0)\" ]"
check "tag deleted" "[ -z \"\$(tagc v2.2.0)\" ]"
rc=$(run 2.2.0 true)
check "re-run succeeds" "[ $rc = 0 ] && [ \"\$(relf v2.2.0 \"['draft']\")\" = False ] && [ \"\$(tagc v2.2.0)\" = \"\$(head_)\" ]"

echo "== failure before tagging leaves nothing (notes would never run)"
bash "$H/commit.sh" "$W" "feature E"
chmod 000 "$W/origin.git/refs/tags" ; rc=$(run 2.3.0 true); chmod 755 "$W/origin.git/refs/tags"
check "tag push failure stops the run" "[ $rc = 1 ] && step_failed 'Create and push the tag' && ! grep -q 'step: Publish the release' $W/last.log"
check "no release, no tag" "[ -z \"\$(rel v2.3.0)\" ] && [ -z \"\$(tagc v2.3.0)\" ]"
check "undo skipped (this run pushed nothing)" "grep -q 'skip: Undo the tag after a failure' $W/last.log"

echo; echo "$fails failed"
exit $fails
