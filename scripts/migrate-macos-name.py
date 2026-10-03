#!/usr/bin/env python3
"""Offline, in-place macOS installation rename. Does not start/stop services."""
import argparse
import os
from pathlib import Path
import plistlib
import subprocess
import sys


def paths(home):
    base = home / 'Library/Application Support'
    agents = home / 'Library/LaunchAgents'
    return (base / 'DarwinRouter', base / 'NexusRouter',
            agents / 'com.darwinrouter.live-test.plist',
            agents / 'com.nexusrouter.live-test.plist')


def transform(value, old, new):
    if isinstance(value, str):
        return value.replace(str(old), str(new)).replace('com.darwinrouter.live-test', 'com.nexusrouter.live-test')
    if isinstance(value, list):
        return [transform(item, old, new) for item in value]
    if isinstance(value, dict):
        return {key: transform(item, old, new) for key, item in value.items()}
    return value


def prepare(home):
    old, new, agent, target = paths(home)
    if old.is_symlink() or not old.is_dir() or os.path.lexists(new) or os.path.lexists(target):
        raise RuntimeError('expected one unmigrated legacy directory and no destination')
    config = old / 'live-test/config.yaml'
    if config.is_symlink() or agent.is_symlink():
        raise RuntimeError('configuration and launch agent must be regular files')
    raw = config.read_bytes()
    plist = plistlib.loads(agent.read_bytes())
    if plist.get('Label') != 'com.darwinrouter.live-test':
        raise RuntimeError('unexpected launch agent identity')
    updated = transform(plist, old, new)
    env = updated.get('EnvironmentVariables', {})
    for key in list(env):
        if key.startswith('DARWIN_'):
            canonical = 'NEXUS_' + key[len('DARWIN_'):]
            if canonical in env and env[canonical] != env[key]:
                raise RuntimeError('conflicting environment aliases')
            env[canonical] = env.pop(key)
    # The installed canonical executable is provided by the validated rollout.
    updated['ProgramArguments'][0] = '/opt/homebrew/bin/nexus'
    return raw.replace(str(old).encode(), str(new).encode()), plistlib.dumps(updated)


def require_idle(old):
    for label in ['com.darwinrouter.live-test', 'com.nexusrouter.live-test']:
        result = subprocess.run(['/bin/launchctl', 'print', f'gui/{os.getuid()}/{label}'], capture_output=True)
        if result.returncode != 113 or b'Could not find service' not in result.stderr:
            raise RuntimeError('service is loaded or its unloaded state could not be verified')
    result = subprocess.run(['/usr/sbin/lsof', '-t', '+D', str(old)], capture_output=True)
    if result.returncode != 1 or result.stdout.strip() or result.stderr.strip():
        raise RuntimeError('data directory has open files or idle inspection failed')
    if not Path('/opt/homebrew/bin/nexus').is_file():
        raise RuntimeError('canonical installed executable missing')


def migrate(home):
    config_body, agent_body = prepare(home)
    old, new, agent, target = paths(home)
    require_idle(old)
    backup = old / 'name-migration-backup'
    backup.mkdir(mode=0o700)  # Existing evidence is never overwritten.
    for name, body in [('config.yaml', (old / 'live-test/config.yaml').read_bytes()),
                       ('launch-agent.plist', agent.read_bytes())]:
        with (backup / name).open('xb') as stream:
            os.chmod(stream.name, 0o600)
            stream.write(body)
            stream.flush()
            os.fsync(stream.fileno())
    old.rename(new)  # Preserve SQLite, skills, permissions and inode identity.
    old.symlink_to(new.name, target_is_directory=True)
    config = new / 'live-test/config.yaml'
    temporary = config.with_name('config.yaml.name-migration')
    with temporary.open('xb') as stream:
        os.chmod(stream.name, 0o600)
        stream.write(config_body)
        stream.flush()
        os.fsync(stream.fileno())
    temporary.replace(config)
    with target.open('xb') as stream:
        os.chmod(stream.name, 0o600)
        stream.write(agent_body)
        stream.flush()
        os.fsync(stream.fileno())
    agent.unlink()  # Private original retained in name-migration-backup.
    for directory in [new, new.parent, target.parent]:
        fd = os.open(directory, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true', help='requires both services unloaded and no open data files')
    args = parser.parse_args()
    try:
        if sys.platform != 'darwin':
            raise RuntimeError('this migration is macOS-only')
        if args.apply:
            migrate(Path.home())
            print('Migration complete; verify configuration before bootstrapping the NexusRouter service.')
        else:
            prepare(Path.home())
            print('Migration plan valid. Data will move in place to Application Support/NexusRouter; a legacy alias preserves guard identity. No changes made.')
    except Exception as exc:
        # Never print configuration or environment values (including secrets).
        print('Migration stopped: ' + type(exc).__name__ + '. Inspect private migration evidence before resuming.', file=sys.stderr)
        sys.exit(1)
