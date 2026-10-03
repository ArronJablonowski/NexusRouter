#!/usr/bin/env python3
"""Consolidate an idle macOS installation under ~/.NexusRouter, preserving aliases."""
import argparse
import json
import os
from pathlib import Path
import plistlib
import shutil
import subprocess
import sys


def inventory(home):
    root = home / '.NexusRouter'
    moves = [(home / 'DarwinRouter', root / 'code'),
             (home / 'Library/Application Support/DarwinRouter/live-test/skills', root / 'skills'),
             (home / 'Library/Application Support/DarwinRouter', root / 'data'),
             (home / '.config/nexusrouter', root / 'config')]
    source = moves[0][0]
    raw = subprocess.check_output(['git', '-C', str(source), 'worktree', 'list', '--porcelain'], text=True)
    for line in raw.splitlines():
        if line.startswith('worktree '):
            path = Path(line[9:])
            if path != source and path.exists():
                moves.append((path, root / 'worktrees' / path.name))
    destinations = [str(dst) for _, dst in moves]
    if len(destinations) != len(set(destinations)):
        raise RuntimeError('worktree destination collision')
    return [(src, dst) for src, dst in moves if src.exists()]


def rewrite(value, moves):
    if isinstance(value, str):
        for src, dst in sorted(moves, key=lambda pair: len(str(pair[0])), reverse=True):
            value = value.replace(str(src), str(dst))
        return value.replace('com.darwinrouter.live-test', 'com.nexusrouter.live-test')
    if isinstance(value, list):
        return [rewrite(item, moves) for item in value]
    if isinstance(value, dict):
        return {key: rewrite(item, moves) for key, item in value.items()}
    return value


def check_idle(moves):
    for label in ['com.darwinrouter.live-test', 'com.nexusrouter.live-test']:
        result = subprocess.run(['/bin/launchctl', 'print', f'gui/{os.getuid()}/{label}'], capture_output=True)
        if result.returncode != 113 or b'Could not find service' not in result.stderr:
            raise RuntimeError('service unloaded state not verified')
    for src, _ in moves:
        result = subprocess.run(['/usr/sbin/lsof', '-t', '+D', str(src)], capture_output=True)
        if result.returncode != 1 or result.stdout.strip() or result.stderr.strip():
            raise RuntimeError('open files or failed idle inspection')


def prepare(home):
    root = home / '.NexusRouter'
    if os.path.lexists(root):
        raise RuntimeError('destination exists; reconcile rather than overwrite')
    moves = inventory(home)
    for src, dst in moves:
        if src.is_symlink() or os.path.lexists(dst):
            raise RuntimeError('unexpected source alias or destination')
    agent = home / 'Library/LaunchAgents/com.darwinrouter.live-test.plist'
    target = agent.with_name('com.nexusrouter.live-test.plist')
    if agent.is_symlink() or os.path.lexists(target):
        raise RuntimeError('unexpected service files')
    config = home / 'Library/Application Support/DarwinRouter/live-test/config.yaml'
    if config.is_symlink():
        raise RuntimeError('unexpected config alias')
    body = rewrite(config.read_text(), moves).encode()
    service = rewrite(plistlib.loads(agent.read_bytes()), moves)
    if service.get('Label') != 'com.nexusrouter.live-test':
        raise RuntimeError('unexpected service label')
    service['ProgramArguments'][0] = str(root / 'bin/nexus')
    env = service.get('EnvironmentVariables', {})
    for key in list(env):
        if key.startswith('DARWIN_'):
            canonical = 'NEXUS_' + key[7:]
            if canonical in env and env[canonical] != env[key]:
                raise RuntimeError('conflicting environment aliases')
            env[canonical] = env.pop(key)
    return moves, body, plistlib.dumps(service)


def private_write(path, body):
    with path.open('xb') as stream:
        os.chmod(path, 0o600)
        stream.write(body)
        stream.flush()
        os.fsync(stream.fileno())


def apply(home, binary):
    moves, config, service = prepare(home)
    check_idle(moves)
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise RuntimeError('validated executable required')
    root = home / '.NexusRouter'
    root.mkdir(mode=0o700)
    backup = root / 'migration-backup'
    backup.mkdir(mode=0o700)
    agent = home / 'Library/LaunchAgents/com.darwinrouter.live-test.plist'
    original_config = home / 'Library/Application Support/DarwinRouter/live-test/config.yaml'
    private_write(backup / 'config.yaml', original_config.read_bytes())
    private_write(backup / 'launch-agent.plist', agent.read_bytes())
    private_write(backup / 'moves.json', json.dumps([[str(a), str(b)] for a, b in moves], indent=2).encode())
    (root / 'bin').mkdir(mode=0o700)
    shutil.copy2(binary, root / 'bin/nexus')
    os.chmod(root / 'bin/nexus', 0o700)
    for src, dst in moves:
        dst.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        src.rename(dst)
        src.symlink_to(dst, target_is_directory=True)
    target_config = root / 'data/live-test/config.yaml'
    temporary = target_config.with_name('config.yaml.consolidation')
    private_write(temporary, config)
    temporary.replace(target_config)
    private_write(agent.with_name('com.nexusrouter.live-test.plist'), service)
    agent.unlink()
    # Aliases keep all registered Git worktree and immutable receipt paths valid.
    # Do not rewrite old receipts or delete aliases while historical work is retained.
    for directory in [root, root / 'data/live-test', agent.parent]:
        fd = os.open(directory, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--binary', type=Path, help='executable built from the fully validated rollout commit')
    args = parser.parse_args()
    try:
        if sys.platform != 'darwin':
            raise RuntimeError('macOS only')
        if args.apply:
            if args.binary is None:
                raise RuntimeError('validated binary required')
            apply(Path.home(), args.binary)
            print('Consolidation complete; verify data and bootstrap only the NexusRouter service.')
        else:
            moves, _, _ = prepare(Path.home())
            print(json.dumps({'destination': str(Path.home() / '.NexusRouter'),
                              'moves': [[str(a), str(b)] for a, b in moves]}, indent=2))
    except Exception as exc:
        print('Consolidation stopped: ' + type(exc).__name__ + '; reconcile private backup before resuming.', file=sys.stderr)
        sys.exit(1)
