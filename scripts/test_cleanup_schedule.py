import base64
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import plistlib
import subprocess
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('schedule', Path(__file__).with_name('cleanup-schedule.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class Launchctl:
    def __init__(self):
        self.loaded = True
        self.disabled = False
        self.running = False
        self.fail = []
        self.calls = []

    def __call__(self, args, **kwargs):
        self.calls.append(args)
        command = args[1]
        if command in self.fail:
            self.fail.remove(command)
            return subprocess.CompletedProcess(args, 5, '', 'injected failure')
        out = ''
        code = 0
        if command == 'print':
            code = 0 if self.loaded else 113
            out = 'state = running\npid = 123\n' if self.running else 'state = not running\n'
        elif command == 'print-disabled':
            out = '{ "com.nexusrouter.cleanup" => ' + str(self.disabled).lower() + ' }'
        elif command == 'disable':
            self.disabled = True
        elif command == 'enable':
            self.disabled = False
        elif command == 'bootout':
            self.loaded = False
        elif command == 'bootstrap':
            self.loaded = True
        else:
            raise AssertionError(args)
        return subprocess.CompletedProcess(args, code, out, '')


class ScheduleTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name).resolve()
        self.root = self.home / '.NexusRouter'
        for p in [self.root / 'tools/maintenance', self.home / 'Library/LaunchAgents']:
            p.mkdir(parents=True, mode=0o700)
        raw = Path(__file__).with_name('com.nexusrouter.cleanup.plist.in').read_text().replace('__NEXUS_HOME__', str(self.root))
        self.agent = self.home / 'Library/LaunchAgents/com.nexusrouter.cleanup.plist'
        self.agent.write_text(raw)
        self.agent.chmod(0o600)
        self.os = Launchctl()
        self.s = m.Scheduler(self.root, runner=self.os, user_home=self.home)
        self.original = self.agent.read_bytes()
        (self.root / 'tools/maintenance/qa-builds.json').write_text('["preserve"]')

    def settings(self, enabled=True, hour=4, minute=15, weekday=None):
        return dict(enabled=enabled, hour=hour, minute=minute, weekday=weekday)

    def apply(self, settings):
        return self.s.apply(settings, self.s.status()['schedule']['digest'])

    def test_daily_weekly_and_immediate_disable_enable(self):
        for settings in [self.settings(), self.settings(weekday=2), self.settings(False, weekday=2), self.settings(True, 3, 30)]:
            value = self.apply(settings)['schedule']
            for key in settings:
                self.assertEqual(value[key], settings[key])
            self.assertEqual(value['state'], 'ready')
            self.assertEqual(self.os.loaded, settings['enabled'])
            self.assertEqual(self.os.disabled, not settings['enabled'])
        self.assertFalse(self.s.pending.exists())
        self.assertEqual((self.root / 'tools/maintenance/qa-builds.json').read_text(), '["preserve"]')
        self.assertFalse(any('commander' in ' '.join(x) for x in self.os.calls))
        self.assertFalse(any(x[1] == 'kickstart' for x in self.os.calls))
        self.assertNotIn('RunAtLoad', plistlib.loads(self.agent.read_bytes()))

    def test_conflict_has_no_scheduler_mutations(self):
        digest = self.s.status()['schedule']['digest']
        self.apply(self.settings())
        start = len(self.os.calls)
        with self.assertRaises(m.Changed):
            self.s.apply(self.settings(False), digest)
        self.assertTrue(all(x[1] in ['print', 'print-disabled'] for x in self.os.calls[start:]))

    def test_activation_failure_rolls_back(self):
        self.os.fail = ['bootstrap']
        with self.assertRaises(RuntimeError):
            self.apply(self.settings(weekday=4))
        self.assertEqual(self.agent.read_bytes(), self.original)
        self.assertTrue(self.os.loaded)
        self.assertFalse(self.os.disabled)
        self.assertFalse(self.s.pending.exists())

    def test_failed_rollback_is_recovered_on_explicit_save(self):
        self.os.fail = ['bootstrap', 'bootstrap']
        with self.assertRaises(RuntimeError):
            self.apply(self.settings())
        self.assertTrue(self.s.pending.exists())
        value = self.s.status()
        self.assertEqual(value['schedule']['state'], 'needs_recovery')
        result = self.s.apply(self.settings(False), value['schedule']['digest'])
        self.assertTrue(result['recovered'])
        self.assertEqual(self.agent.read_bytes(), self.original)
        self.assertEqual(result['schedule']['hour'], 3)
        self.assertTrue(result['schedule']['enabled'])

    def test_crash_intent_requires_explicit_recovery(self):
        m.c.atomic_json(self.s.pending, {'previous': base64.b64encode(self.original).decode(), 'loaded': True, 'disabled': False})
        self.assertEqual(self.s.status()['schedule']['state'], 'needs_recovery')
        self.assertTrue(self.s.pending.exists())
        self.assertTrue(self.apply(self.settings(False))['recovered'])

    def test_invalid_recovery_never_changes_scheduler_or_agent(self):
        valid = {'previous': base64.b64encode(self.original).decode(), 'loaded': True, 'disabled': False}
        bad_agent = plistlib.loads(self.original)
        bad_agent['ProgramArguments'] = ['/bin/echo', 'invalid']
        bad_calendar = plistlib.loads(self.original)
        bad_calendar['StartCalendarInterval'] = {'Hour': 25, 'Minute': 0}
        cases = [{**valid, 'previous': base64.b64encode(plistlib.dumps(value)).decode()}
                 for value in [bad_agent, bad_calendar]]
        cases += [{**valid, 'loaded': 1}, {**valid, 'disabled': 'false'},
                  {**valid, 'unknown': True}, {**valid, 'previous': '!'}]
        for transaction in cases:
            with self.subTest(transaction=transaction):
                self.os.calls.clear()
                m.c.atomic_json(self.s.pending, transaction)
                with self.assertRaises(ValueError):
                    self.apply(self.settings(False))
                self.assertEqual(self.agent.read_bytes(), self.original)
                self.assertTrue(self.os.loaded)
                self.assertFalse(self.os.disabled)
                self.assertTrue(all(x[1] in ['print', 'print-disabled'] for x in self.os.calls))
                self.assertTrue(self.s.pending.exists())

    def test_running_cleanup_recovery_has_no_mutations(self):
        m.c.atomic_json(self.s.pending, {'previous': base64.b64encode(self.original).decode(), 'loaded': True, 'disabled': False})
        self.os.running = True
        with self.assertRaises(RuntimeError):
            self.apply(self.settings(False))
        self.assertEqual(self.agent.read_bytes(), self.original)
        self.assertFalse(self.os.disabled)
        self.assertTrue(self.s.pending.exists())
        self.assertTrue(all(x[1] in ['print', 'print-disabled'] for x in self.os.calls))

    def test_running_cleanup_never_stopped(self):
        self.os.running = True
        with self.assertRaises(RuntimeError):
            self.apply(self.settings(False))
        self.assertEqual(self.agent.read_bytes(), self.original)
        self.assertTrue(all(x[1] in ['print', 'print-disabled'] for x in self.os.calls))

    def test_shared_lock_excludes_schedule_changes(self):
        with (self.s.state / 'cleanup.lock').open('a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaises(BlockingIOError):
                self.apply(self.settings())
        self.assertEqual(self.agent.read_bytes(), self.original)

    def test_invalid_fields_and_agent_fail_closed(self):
        for settings in [self.settings(hour=24), self.settings(minute=-1), self.settings(weekday=7), self.settings(enabled=1), self.settings(hour=True)]:
            with self.assertRaises(ValueError):
                self.apply(settings)
        value = plistlib.loads(self.original)
        value['ProgramArguments'] = ['/bin/sh', '-c', 'unsafe']
        self.agent.write_bytes(plistlib.dumps(value))
        with self.assertRaises(ValueError):
            self.s.status()
        self.assertFalse(any(x[1] in ['disable', 'bootout'] for x in self.os.calls))

    def test_symlink_and_writable_agent_rejected(self):
        self.agent.chmod(0o666)
        with self.assertRaises(ValueError):
            self.s.status()
        self.agent.unlink()
        target = self.home / 'other'
        target.write_bytes(self.original)
        self.agent.symlink_to(target)
        with self.assertRaises(ValueError):
            self.s.status()

    def test_unknown_scheduler_failure_not_treated_as_unloaded(self):
        self.os.fail = ['print']
        with self.assertRaises(RuntimeError):
            self.s.status()

    def test_external_disable_is_visible_and_reconciled(self):
        self.os.disabled = True
        self.assertEqual(self.s.status()['schedule']['state'], 'needs_attention')
        self.assertEqual(self.apply(self.settings())['schedule']['state'], 'ready')

    def test_pending_symlink_never_followed(self):
        target = self.home / 'other'
        target.write_text('{}')
        self.s.pending.symlink_to(target)
        with self.assertRaises(ValueError):
            self.apply(self.settings())
        self.assertEqual(target.read_text(), '{}')

    def test_rollback_preserves_loaded_external_disable_override(self):
        self.os.disabled = True
        self.os.fail = ['bootstrap']
        with self.assertRaises(RuntimeError):
            self.apply(self.settings())
        self.assertTrue(self.os.loaded)
        self.assertTrue(self.os.disabled)
        self.assertEqual(self.agent.read_bytes(), self.original)

    def test_disable_failure_restores_previous_schedule(self):
        self.os.fail = ['disable']
        with self.assertRaises(RuntimeError):
            self.apply(self.settings(False))
        self.assertTrue(self.os.loaded)
        self.assertFalse(self.os.disabled)
        self.assertFalse(self.s.pending.exists())

    def test_modern_launchctl_state_words(self):
        original = self.os.__call__
        def runner(args, **kwargs):
            result = original(args, **kwargs)
            if args[1] == 'print-disabled':
                result.stdout = result.stdout.replace('true', 'disabled').replace('false', 'enabled')
            return result
        self.s.runner = runner
        self.assertEqual(self.apply(self.settings(False))['schedule']['state'], 'ready')
        self.assertEqual(self.apply(self.settings(True))['schedule']['state'], 'ready')


if __name__ == '__main__':
    unittest.main()
