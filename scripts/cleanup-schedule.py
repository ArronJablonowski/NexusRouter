#!/usr/bin/env python3
"""Manage only NexusRouter's cleanup LaunchAgent; never restart the commander."""
import argparse
import base64
import datetime
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import plistlib
import re
import subprocess
import sys

_spec = importlib.util.spec_from_file_location('cleanup', Path(__file__).with_name('cleanup-nexus-home.py'))
c = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(c)
LABEL = 'com.nexusrouter.cleanup'


class Changed(ValueError):
    pass


class Scheduler:
    def __init__(self, root, runner=subprocess.run, user_home=None):
        self.root = Path(root).absolute()
        self.user_home = Path(user_home) if user_home else Path.home()
        self.state = self.root / 'tools/maintenance'
        self.agent = self.user_home / 'Library/LaunchAgents' / (LABEL + '.plist')
        self.pending = self.state / 'schedule-transaction.json'
        self.domain = 'gui/' + str(os.getuid())
        self.job = self.domain + '/' + LABEL
        self.runner = runner
        for p in [self.root, self.root / 'tools', self.state, self.agent.parent]:
            c.private_dir(p)
            if p.resolve() != p:
                raise ValueError('scheduler path contains a symlink')

    def command(self, *args, allow_missing=False):
        result = self.runner(['/bin/launchctl', *args], capture_output=True, text=True, timeout=10)
        if result.returncode and not (allow_missing and result.returncode == 113):
            raise RuntimeError('cleanup scheduler command failed')
        return result

    def read(self):
        s = self.agent.lstat()
        if not self.agent.is_file() or self.agent.is_symlink() or s.st_uid != os.getuid() or s.st_mode & 0o022 or s.st_size > 16384:
            raise ValueError('unsafe cleanup LaunchAgent')
        raw = self.agent.read_bytes()
        value = plistlib.loads(raw)
        expected = ['/usr/bin/python3', '-B', str(self.state / 'cleanup-nexus-home.py'), '--home', str(self.root), '--apply']
        allowed = {'Label', 'ProgramArguments', 'StartCalendarInterval', 'ProcessType', 'Nice', 'LowPriorityIO', 'StandardOutPath', 'StandardErrorPath', 'Disabled'}
        if not isinstance(value, dict) or set(value) - allowed or value.get('Label') != LABEL or value.get('ProgramArguments') != expected:
            raise ValueError('unrecognized cleanup LaunchAgent')
        if 'Disabled' in value and type(value['Disabled']) is not bool:
            raise ValueError('invalid cleanup enabled state')
        cal = value.get('StartCalendarInterval')
        if not isinstance(cal, dict) or set(cal) - {'Hour', 'Minute', 'Weekday'} or not {'Hour', 'Minute'} <= set(cal):
            raise ValueError('unsupported cleanup calendar')
        self.validate({'enabled': not value.get('Disabled', False), 'hour': cal['Hour'], 'minute': cal['Minute'], 'weekday': cal.get('Weekday')})
        if value.get('StandardOutPath') != str(self.state / 'scheduler.log') or value.get('StandardErrorPath') != str(self.state / 'scheduler-error.log'):
            raise ValueError('unrecognized cleanup log path')
        return raw, value

    @staticmethod
    def validate(settings):
        if set(settings) != {'enabled', 'hour', 'minute', 'weekday'} or type(settings['enabled']) is not bool:
            raise ValueError('invalid cleanup schedule')
        for key, high in [('hour', 23), ('minute', 59)]:
            if type(settings[key]) is not int or not 0 <= settings[key] <= high:
                raise ValueError('invalid cleanup time')
        day = settings['weekday']
        if day is not None and (type(day) is not int or not 0 <= day <= 6):
            raise ValueError('invalid cleanup weekday')

    def observed(self):
        result = self.command('print', self.job, allow_missing=True)
        loaded = result.returncode == 0
        # Do not boot out a running cleanup, even before it acquires its lock.
        running = bool(re.search(r'^\s*pid = \d+\s*$', result.stdout, re.M)) if loaded else False
        disabled = self.command('print-disabled', self.domain).stdout
        match = re.search(r'"' + re.escape(LABEL) + r'"\s*=>\s*(\w+)', disabled)
        if match and match.group(1) not in ('true', 'false', 'enabled', 'disabled'):
            raise RuntimeError('unrecognized cleanup scheduler state')
        return loaded, running, match.group(1) in ('true', 'disabled') if match else False

    def status(self):
        raw, value = self.read()
        loaded, running, disabled = self.observed()
        enabled = not value.get('Disabled', False)
        healthy = (loaded and not disabled) if enabled else (not loaded and disabled)
        cal = value['StartCalendarInterval']
        return {'version': 1, 'schedule': {'enabled': enabled, 'hour': cal['Hour'], 'minute': cal['Minute'], 'weekday': cal.get('Weekday'),
                'loaded': loaded, 'running': running, 'state': 'needs_recovery' if self.pending.exists() else ('ready' if healthy else 'needs_attention'),
                'timezone': datetime.datetime.now().astimezone().tzname(),
                'digest': hashlib.sha256(raw + json.dumps([loaded, disabled]).encode()).hexdigest()}, 'recovered': False}

    def write_agent(self, raw):
        # Atomic same-directory publication, mode 0600; no follow of temporary links.
        fd, name = c.tempfile.mkstemp(prefix='.nexus-cleanup-', dir=str(self.agent.parent))
        with os.fdopen(fd, 'wb') as f:
            f.write(raw)
            f.flush()
            os.fsync(f.fileno())
        os.replace(name, str(self.agent))

    def unload(self):
        loaded, running, _ = self.observed()
        if running:
            raise RuntimeError('cleanup is running; schedule unchanged')
        if loaded:
            self.command('bootout', self.job)

    def restore(self, transaction):
        raw = base64.b64decode(transaction['previous'], validate=True)
        value = plistlib.loads(raw)
        # The transaction can only restore our own validated executable/calendar.
        if value.get('Label') != LABEL or len(raw) > 16384:
            raise ValueError('invalid scheduler recovery record')
        self.command('disable', self.job)
        self.unload()
        self.write_agent(raw)
        self.read()
        self.command('enable' if transaction['loaded'] or not transaction['disabled'] else 'disable', self.job)
        if transaction['loaded']:
            self.command('bootstrap', self.domain, str(self.agent))
        if transaction['disabled']:
            self.command('disable', self.job)
        loaded, _, disabled = self.observed()
        if loaded != transaction['loaded'] or disabled != transaction['disabled']:
            raise RuntimeError('scheduler recovery incomplete')
        self.pending.unlink()

    def apply(self, settings, expected):
        self.validate(settings)
        if not re.fullmatch(r'[0-9a-f]{64}', expected):
            raise ValueError('invalid schedule digest')
        fd = os.open(str(self.state / 'cleanup.lock'), os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        with os.fdopen(fd, 'a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            if self.pending.is_symlink():
                raise ValueError('invalid scheduler recovery record')
            if self.pending.exists():
                if self.pending.is_symlink() or self.pending.stat().st_size > 32768:
                    raise ValueError('invalid scheduler recovery record')
                self.restore(json.loads(self.pending.read_text()))
                out = self.status()
                out['recovered'] = True
                return out
            current = self.status()
            if current['schedule']['digest'] != expected:
                raise Changed('cleanup schedule changed')
            raw, value = self.read()
            loaded, running, disabled = self.observed()
            if running:
                raise RuntimeError('cleanup is running; schedule unchanged')
            transaction = {'previous': base64.b64encode(raw).decode(), 'loaded': loaded, 'disabled': disabled}
            c.atomic_json(self.pending, transaction)
            try:
                self.command('disable', self.job)
                self.unload()
                value['Disabled'] = not settings['enabled']
                value['StartCalendarInterval'] = {'Hour': settings['hour'], 'Minute': settings['minute']}
                if settings['weekday'] is not None:
                    value['StartCalendarInterval']['Weekday'] = settings['weekday']
                self.write_agent(plistlib.dumps(value))
                if settings['enabled']:
                    self.command('enable', self.job)
                    self.command('bootstrap', self.domain, str(self.agent))
                check = self.status()['schedule']
                if check['loaded'] != settings['enabled'] or (not self.observed()[2]) != settings['enabled']:
                    raise RuntimeError('scheduler activation incomplete: ' + repr([check['loaded'], self.observed()[2], settings['enabled']]))
                self.pending.unlink()
                return self.status()
            except Exception:
                self.restore(transaction)
                raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--home', type=Path, required=True)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    try:
        scheduler = Scheduler(args.home)
        if args.apply:
            raw = sys.stdin.read(4097)
            if len(raw) > 4096:
                raise ValueError('request too large')
            value = json.loads(raw)
            if set(value) != {'settings', 'expected_digest'}:
                raise ValueError('invalid request')
            out = scheduler.apply(value['settings'], value['expected_digest'])
        else:
            out = scheduler.status()
        print(json.dumps(out))
        return 0
    except Changed:
        print(json.dumps({'error': 'schedule_changed'}))
        return 3
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError, KeyError, TypeError):
        print(json.dumps({'error': 'schedule_unavailable'}))
        return 1


if __name__ == '__main__':
    sys.exit(main())
