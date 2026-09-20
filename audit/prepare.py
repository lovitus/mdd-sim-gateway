import base64
import hashlib
import json
import lzma
import os
from pathlib import Path
import subprocess
import sys
import urllib.request

BASE = '3e6d5db7657e468eedc0c31316bb97c04b2b599b'
BASE_TREE = 'f0f6b038519bdce6520ea805c4c77c5aecd0535a'
PAYLOAD_SHA = 'dbf59c7754bd58d7de79f85b52d1a7a73c95a46a300eae86f9ab1b8f0494dc2f'
SOURCE_TREE = 'f8ddb2c3ee0280d8b48341480e8a8bee718c61b7'
FINAL_TREE = 'd14d6e020f537d72802f66701eec8088da2ec2f1'
REPO = 'lovitus/mdd-sim-gateway'

mode, candidate_arg, payload_dir_arg, evidence_arg = sys.argv[1:]
root = Path(candidate_arg).resolve()
evidence = Path(evidence_arg).resolve()
evidence.mkdir(parents=True, exist_ok=True)
def git(*args):
    return subprocess.check_output(['git', '-C', str(root), *args]).decode().strip()
def safe(name):
    relative = Path(name)
    if relative.is_absolute() or not relative.parts or any(p in ('.', '..', '.git') for p in relative.parts):
        raise ValueError('unsafe candidate path')
    target = root / relative
    if not target.resolve().is_relative_to(root):
        raise ValueError('candidate path escapes checkout')
    return target

def api(endpoint, body):
    url = 'https://api.github.com/repos/' + REPO + '/git/' + endpoint
    request = urllib.request.Request(url, data=json.dumps(body).encode(), method='POST', headers={
        'Authorization': 'Bearer ' + os.environ['GH_TOKEN'],
        'Accept': 'application/vnd.github+json',
        'Content-Type': 'application/json',
        'X-GitHub-Api-Version': '2022-11-28',
    })
    with urllib.request.urlopen(request, timeout=60) as response:
        return json.load(response)

if mode == 'apply':
    assert git('rev-parse', 'HEAD') == BASE
    assert git('rev-parse', 'HEAD^{tree}') == BASE_TREE
    assert not git('status', '--porcelain')
    pieces = sorted(Path(payload_dir_arg).glob('payload.*'))
    assert len(pieces) == 7
    payload = b''.join(p.read_bytes() for p in pieces)
    assert len(payload) == 49752 and hashlib.sha256(payload).hexdigest() == PAYLOAD_SHA
    plan = json.loads(lzma.decompress(payload))
    assert (plan['baseline'], plan['baselineTree'], plan['sourceTree'], plan['finalTree']) == (BASE, BASE_TREE, SOURCE_TREE, FINAL_TREE)
    assert len(plan['copies']) == 6 and len(plan['remove']) == 68 and len(plan['newFiles']) == 14
    for copy in plan['copies']:
        data = safe(copy['source']).read_bytes()
        assert hashlib.sha256(data).hexdigest() == copy['sha256']
        target = safe(copy['target'])
        assert not target.exists()
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
    patch = evidence / 'reviewed-source.patch'
    patch.write_text(plan['patch'])
    subprocess.run(['git', '-C', str(root), 'apply', '--check', str(patch)], check=True)
    subprocess.run(['git', '-C', str(root), 'apply', str(patch)], check=True)
    for name in plan['remove']:
        target = safe(name)
        assert target.is_file()
        target.unlink()
    for name, content in plan['newFiles'].items():
        target = safe(name)
        assert not target.exists()
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content)
    subprocess.run(['node', 'tools/repository-check.mjs', '--write'], cwd=root, check=True)
    subprocess.run(['git', '-C', str(root), 'add', '-A'], check=True)
    assert git('write-tree') == SOURCE_TREE, git('write-tree')
    (evidence / 'source-identity.json').write_text(json.dumps({'baseline': BASE, 'baselineTree': BASE_TREE, 'sourceTree': SOURCE_TREE, 'finalTree': FINAL_TREE, 'payloadSHA256': PAYLOAD_SHA}, indent=2) + '\n')
    print('Verified exact reviewed source tree:', SOURCE_TREE)
elif mode == 'publish-objects':
    assert git('rev-parse', 'HEAD') == BASE
    subprocess.run(['git', '-C', str(root), 'add', '-u', '--', 'go-runtime/internal/webui/assets'], check=True)
    subprocess.run(['git', '-C', str(root), 'diff', '--cached', '--check'], check=True)
    assert not git('diff'), 'candidate has unstaged changes'
    assert git('write-tree') == FINAL_TREE, git('write-tree')
    changes = [line.split('\t', 1) for line in git('diff', '--cached', '--no-renames', '--name-status').splitlines()]
    assert len(changes) == 116
    entries, uploaded = [], set()
    for status, name in changes:
        safe(name)
        if status == 'D':
            entries.append({'path': name, 'mode': '100644', 'type': 'blob', 'sha': None})
            continue
        indexed = git('ls-files', '--stage', '--', name).split()
        file_mode, sha = indexed[:2]
        assert file_mode in ('100644', '100755')
        if sha not in uploaded:
            data = safe(name).read_bytes()
            assert hashlib.sha1(('blob ' + str(len(data)) + '\0').encode() + data).hexdigest() == sha
            result = api('blobs', {'content': base64.b64encode(data).decode(), 'encoding': 'base64'})
            assert result['sha'] == sha
            uploaded.add(sha)
        entries.append({'path': name, 'mode': file_mode, 'type': 'blob', 'sha': sha})
    tree = api('trees', {'base_tree': BASE_TREE, 'tree': entries})
    assert tree['sha'] == FINAL_TREE
    commit = api('commits', {'message': 'fix: unify mounted UI state and version acceptance evidence\n\nAddress the verified repository findings in issue #3. Retain every original acceptance criterion, remove unmounted UI and unused legacy resources, preserve license/data provenance, and add full Core race and repository guards. Product/HIL work remains explicitly open.', 'tree': FINAL_TREE, 'parents': [BASE]})
    (evidence / 'published-objects.json').write_text(json.dumps({'commit': commit['sha'], 'tree': tree['sha'], 'baseline': BASE, 'changedFiles': len(changes)}, indent=2) + '\n')
    with (evidence / 'tested-fix.patch').open('wb') as output:
        subprocess.run(['git', '-C', str(root), 'diff', '--cached', '--binary'], stdout=output, check=True)
    print('Verified GitHub commit object:', commit['sha'], 'tree:', FINAL_TREE)
    print('No branch or main ref moved; PR creation is a separate verified operation.')
else:
    raise ValueError('unknown preparation mode')
