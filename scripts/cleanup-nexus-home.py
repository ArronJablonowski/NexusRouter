#!/usr/bin/env python3
"""Conservative, opt-in NexusRouter maintenance. Dry-run unless --apply."""
import argparse
import contextlib
import datetime
import fcntl
import json
import os
from pathlib import Path
import plistlib
import re
import sqlite3
import stat
import subprocess
import sys
import tempfile
import time

MAGIC = {b'\x7fELF', b'\xcf\xfa\xed\xfe', b'\xfe\xed\xfa\xcf', b'\xca\xfe\xba\xbe', b'\xbe\xba\xfe\xca'}
BACKUP = re.compile(r'^\d{8}T\d{6}Z-[A-Za-z0-9_-]+$')
DAY = 86400


def identity(path):
    s = path.lstat()
    return (s.st_dev, s.st_ino, s.st_mode, s.st_size, s.st_mtime_ns)


def safe_tree(path, device):
    """Reject links, special files, foreign ownership and mounted subtrees."""
    result = {}
    for base, dirs, files in os.walk(str(path), followlinks=False):
        for p in [Path(base)] + [Path(base) / n for n in dirs + files]:
            s = p.lstat()
            if (stat.S_ISREG(s.st_mode) and s.st_nlink != 1) or (stat.S_ISDIR(s.st_mode) and s.st_mode & 0o022) or s.st_uid != os.getuid() or s.st_dev != device or not (
                stat.S_ISREG(s.st_mode) or stat.S_ISDIR(s.st_mode)
            ):
                raise ValueError('unsafe tree: ' + str(p))
            result[str(p)] = identity(p)
    return result


def native(path):
    s = path.lstat()
    if not stat.S_ISREG(s.st_mode) or s.st_uid != os.getuid() or s.st_nlink != 1:
        return False
    with path.open('rb') as stream:
        return stream.read(4) in MAGIC


def private_dir(path):
    s = path.lstat()
    if not stat.S_ISDIR(s.st_mode) or s.st_uid != os.getuid() or s.st_mode & 0o022:
        raise ValueError('maintenance requires an owned, non-writable-by-others directory: ' + str(path))


def qa_path(root, relative):
    p = Path(relative)
    if len(p.parts) != 3 or p.parts[0] != 'resources' or any(x in ('.', '..') for x in p.parts):
        raise ValueError('QA path must be resources/<run>/<binary>')
    if p.parts[1] == 'nexusrouter-development' or not re.fullmatch(r'nexus[\w.-]*', p.name):
        raise ValueError('not a disposable QA binary path')
    directory = root / p.parent
    if not directory.exists() and not directory.is_symlink():
        return root / p
    private_dir(directory)
    if directory.resolve() != directory:
        raise ValueError('QA directory contains a symlink')
    return root / p


def inventory(root):
    """Only retain path metadata under NexusRouter; never read process environments."""
    proc = subprocess.run(['/usr/sbin/lsof', '-nP', '-F', 'pn'], capture_output=True, text=True, timeout=60)
    if proc.returncode != 0 or proc.stderr.strip():
        raise RuntimeError('open-file inventory incomplete; no cleanup performed')
    paths = [line[1:] for line in proc.stdout.splitlines() if line.startswith('n' + str(root) + '/')]
    for directory in [Path.home() / 'Library/LaunchAgents', Path('/Library/LaunchAgents'), Path('/Library/LaunchDaemons')]:
        if not directory.exists():
            continue
        for path in directory.glob('*nexus*.plist'):
            with path.open('rb') as stream:
                value = plistlib.load(stream)
            own_arguments = ['/usr/bin/python3', '-B', str(root / 'tools/maintenance/cleanup-nexus-home.py'), '--home', str(root), '--apply']
            if value.get('Label') == 'com.nexusrouter.cleanup' and value.get('ProgramArguments') == own_arguments:
                continue  # Our own --home argument is not an artifact lease.
            paths.extend(str(x) for x in value.get('ProgramArguments', []) if str(root) in str(x))
            paths.extend(str(value.get(k, '')) for k in ('Program', 'WorkingDirectory'))
    # Protect scripts/builds mentioned by active worker command lines, including dormant child work.
    processes = subprocess.run(['/bin/ps', '-axo', 'command='], capture_output=True, text=True, timeout=15, check=True)
    return paths, [line for line in processes.stdout.splitlines() if str(root) in line]


def used(path, snapshot):
    paths, commands = snapshot
    value = str(path)
    return any(p == value or p.startswith(value + '/') or value.startswith(p.rstrip('/') + '/') for p in paths if p) or any(
        value in line for line in commands
    )


def verify_backup(path, device):
    safe_tree(path, device)
    if not native(path / 'nexus.previous') or not (path / 'config').is_dir():
        raise ValueError('incomplete rollback snapshot')
    db = path / 'commander/tasks.db'
    if not db.is_file():
        raise ValueError('missing rollback database')
    if Path(str(db) + '-wal').exists() and Path(str(db) + '-wal').stat().st_size:
        raise ValueError('rollback database has an uncheckpointed WAL')
    with contextlib.closing(sqlite3.connect(db.as_uri() + '?mode=ro&immutable=1', uri=True, timeout=10)) as connection:
        connection.execute('PRAGMA busy_timeout=10000')
        if connection.execute('PRAGMA quick_check').fetchall() != [('ok',)]:
            raise ValueError('rollback database failed integrity check')


def plan(root, registry, snapshot, now):
    device = root.stat().st_dev
    backups = root / 'bin/backups'
    private_dir(backups)
    if backups.resolve() != backups:
        raise ValueError('backup path contains a symlink')
    retained = sorted([p for p in backups.iterdir() if BACKUP.fullmatch(p.name)], key=lambda p: p.name)[-3:]
    result = {'retained_backups': [str(p) for p in retained], 'candidates': [], 'skipped': []}
    all_backups = sorted(p for p in backups.iterdir() if BACKUP.fullmatch(p.name))
    try:
        if len(retained) != 3:
            raise ValueError('fewer than three rollback snapshots')
        for p in retained:
            verify_backup(p, device)
        # Avoid racing a deployment that has begun a fresh/incomplete snapshot.
        if any(now - p.lstat().st_mtime < 3600 for p in all_backups):
            raise ValueError('deployment backup grace period (1 hour)')
        for p in all_backups[:-3]:
            verify_backup(p, device)
            if used(p, snapshot):
                result['skipped'].append({'path': str(p), 'reason': 'in use or configured'})
            else:
                result['candidates'].append({'path': str(p), 'kind': 'backup', 'tree': safe_tree(p, device)})
    except (OSError, ValueError, sqlite3.Error) as error:
        result['candidates'] = []
        result['skipped'].append({'path': str(backups), 'reason': str(error)})
    if not isinstance(registry, list) or any(not isinstance(x, str) for x in registry):
        raise ValueError('invalid QA registry')
    for relative in sorted(set(registry)):
        p = qa_path(root, relative)
        if not p.exists() and not p.is_symlink():
            continue
        try:
            if not native(p) or p.stat().st_size < 20 * 1024 * 1024:
                raise ValueError('not a registered native build of at least 20 MiB')
            if now - p.stat().st_mtime < 7 * DAY:
                raise ValueError('QA build grace period (7 days)')
            if used(p, snapshot) or any(str(p.parent) in line for line in snapshot[1]):
                raise ValueError('in use or configured')
            result['candidates'].append({'path': str(p), 'kind': 'qa', 'tree': {str(p): identity(p)}})
        except (OSError, ValueError) as error:
            result['skipped'].append({'path': str(p), 'reason': str(error)})
    return result


def remove_tree(path, expected):
    """Anchor every deletion to owned directory descriptors; never follow links."""
    def remove(parent_fd, name, absolute):
        before = os.stat(name, dir_fd=parent_fd, follow_symlinks=False)
        fingerprint = (before.st_dev, before.st_ino, before.st_mode, before.st_size, before.st_mtime_ns)
        if fingerprint != tuple(expected[absolute]):
            raise RuntimeError('candidate changed; stopped')
        if stat.S_ISDIR(before.st_mode):
            fd = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=parent_fd)
            try:
                if os.fstat(fd).st_ino != before.st_ino:
                    raise RuntimeError('directory changed; stopped')
                names = os.listdir(fd)
                expected_names = {Path(p).name for p in expected if str(Path(p).parent) == absolute}
                if set(names) != expected_names:
                    raise RuntimeError('directory contents changed; stopped')
                for child in names:
                    remove(fd, child, str(Path(absolute) / child))
            finally:
                os.close(fd)
            os.rmdir(name, dir_fd=parent_fd)
        else:
            os.unlink(name, dir_fd=parent_fd)
    fd = os.open(str(path.parent), os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        remove(fd, path.name, str(path))
    finally:
        os.close(fd)


def atomic_json(path, value):
    fd, name = tempfile.mkstemp(prefix='.cleanup-', dir=str(path.parent))
    temporary = Path(name)
    if 'candidates' in value:
        value = dict(value, candidates=[{'path': x['path'], 'kind': x['kind']} for x in value['candidates']])
    with os.fdopen(fd, 'w') as stream:
        json.dump(value, stream, indent=2)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(str(temporary), str(path))


def run(root, apply=False, register=None, probe=inventory, now=None):
    root = Path(root).absolute()
    if root.resolve() != root:
        raise ValueError('home must not contain symlinks')
    private_dir(root)
    private_dir(root / 'resources')
    tools = root / 'tools'
    tools.mkdir(exist_ok=True, mode=0o700)
    private_dir(tools)
    state = tools / 'maintenance'
    state.mkdir(exist_ok=True, mode=0o700)
    private_dir(state)
    lockfd = os.open(str(state / 'cleanup.lock'), os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    with os.fdopen(lockfd, 'a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        registry_path = state / 'qa-builds.json'
        if registry_path.is_symlink():
            raise ValueError('registry must not be a symlink')
        registry = json.loads(registry_path.read_text()) if registry_path.exists() else []
        if register:
            p = qa_path(root, register)
            if not native(p) or p.stat().st_size < 20 * 1024 * 1024:
                raise ValueError('only native QA binaries of at least 20 MiB may be registered')
            registry = sorted(set(registry + [register]))
            atomic_json(registry_path, registry)
            return {'registered': register}
        snapshot = probe(root)  # Fail before constructing/deleting any candidates.
        result = plan(root, registry, snapshot, time.time() if now is None else now)
        result.update({'apply': apply, 'deleted': [], 'errors': [], 'time': datetime.datetime.now(datetime.timezone.utc).isoformat()})
        journal = state / 'last-cleanup.json'
        # Persist intent before any unlink. Interrupted runs are never replayed; re-plan next time.
        if apply:
            atomic_json(journal, result)
            for item in result['candidates']:
                p = Path(item['path'])
                try:
                    fresh = probe(root)
                    if used(p, fresh) or (item['kind'] == 'qa' and any(str(p.parent) in line for line in fresh[1])):
                        raise RuntimeError('candidate now in use; skipped')
                    if item['kind'] == 'backup':
                        # A new deployment or changed retained copy invalidates the original plan.
                        current = plan(root, [], fresh, time.time() if now is None else now)
                        if current['retained_backups'] != result['retained_backups'] or str(p) not in [x['path'] for x in current['candidates']]:
                            raise RuntimeError('backup retention changed; stopped')
                    tree = safe_tree(p, root.stat().st_dev) if item['kind'] == 'backup' else {str(p): identity(p)}
                    if tree != item['tree']:
                        raise RuntimeError('candidate changed; stopped')
                    remove_tree(p, tree)
                    result['deleted'].append(str(p))
                except (OSError, RuntimeError, ValueError) as error:
                    result['errors'].append({'path': str(p), 'reason': str(error)})
                    atomic_json(journal, result)
                    break
                atomic_json(journal, result)
        return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--home', type=Path, default=Path.home() / '.NexusRouter')
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--register-qa', metavar='resources/RUN/nexus-BINARY')
    args = parser.parse_args()
    try:
        result = run(args.home, args.apply, args.register_qa)
        # The detailed private journal contains paths, never credentials or file contents.
        print(json.dumps({k: v for k, v in result.items() if k != 'candidates'}, indent=2))
        if 'candidates' in result:
            print('Eligible items:', len(result['candidates']))
        return 1 if result.get('errors') else 0
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print('Cleanup deferred: ' + str(error), file=sys.stderr)
        return 1


if __name__ == '__main__':
    sys.exit(main())
