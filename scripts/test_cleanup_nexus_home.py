"""Destructive maintenance tests use only isolated temporary homes."""
import contextlib
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import time
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('cleanup', Path(__file__).with_name('cleanup-nexus-home.py'))
c = importlib.util.module_from_spec(spec)
spec.loader.exec_module(c)


class CleanupTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve() / '.NexusRouter'
        for relative in ['', 'resources', 'bin/backups', 'tools/maintenance', 'resources/qa-test']:
            (self.root / relative).mkdir(parents=True, exist_ok=True, mode=0o700)
        self.now = time.time()
        self.probe = lambda root: ([], [])
        self.backups = [self.backup(i) for i in range(1, 6)]
        self.build = self.root / 'resources/qa-test/nexus-test'
        self.binary(self.build, large=True)
        c.run(self.root, register=str(self.build.relative_to(self.root)))
        for relative in ['data/commander/tasks.db', 'data/uploads/original.pdf', 'resources/qa-test/REPORT.md', 'resources/qa-test/source.go']:
            p = self.root / relative
            p.parent.mkdir(parents=True, exist_ok=True)
            p.write_text('preserve')

    def binary(self, path, large=False):
        with path.open('wb') as stream:
            stream.write(b'\x7fELF')
            if large:
                stream.truncate(21 * 1024 * 1024)
        os.utime(str(path), (self.now - 10 * c.DAY,) * 2)

    def backup(self, number):
        p = self.root / 'bin/backups' / ('202601%02dT010101Z-test' % number)
        (p / 'commander').mkdir(parents=True)
        (p / 'config').mkdir()
        self.binary(p / 'nexus.previous')
        with contextlib.closing(sqlite3.connect(str(p / 'commander/tasks.db'))) as db:
            db.execute('create table evidence (value text)')
        os.utime(str(p), (self.now - 10 * c.DAY,) * 2)
        return p

    def run_cleanup(self, **kwargs):
        return c.run(self.root, probe=self.probe, now=self.now, **kwargs)

    def test_dry_run_then_real_deletion_and_repeat(self):
        self.assertEqual(len(self.run_cleanup()['candidates']), 3)
        self.assertTrue(self.build.exists())
        result = self.run_cleanup(apply=True)
        self.assertEqual(len(result['deleted']), 3)
        self.assertFalse(result['errors'])
        self.assertEqual([p for p in self.backups if p.exists()], self.backups[-3:])
        self.assertFalse(self.build.exists())
        self.assertEqual(len(self.run_cleanup(apply=True)['deleted']), 0)
        for p in self.root.rglob('*'):
            if p.name in ('original.pdf', 'REPORT.md', 'source.go') or str(p).endswith('data/commander/tasks.db'):
                self.assertEqual(p.read_text(), 'preserve')

    def test_open_files_cwd_configured_and_active_worker(self):
        for snapshot in [([str(self.build)], []), ([str(self.build.parent)], []), ([], ['python ' + str(self.build.parent / 'worker.py')])]:
            self.probe = lambda root: snapshot
            self.assertNotIn(str(self.build), [x['path'] for x in self.run_cleanup()['candidates']])
        self.probe = lambda root: ([str(self.backups[0] / 'commander/tasks.db')], [])
        self.assertTrue(self.backups[0].exists())
        self.assertNotIn(str(self.backups[0]), [x['path'] for x in self.run_cleanup()['candidates']])

    def test_recent_build_and_deployment_grace(self):
        os.utime(str(self.build), None)
        os.utime(str(self.backups[-1]), None)
        self.assertEqual(self.run_cleanup()['candidates'], [])

    def test_incomplete_or_corrupt_retained_copy_blocks_backup_pruning(self):
        db = self.backups[-1] / 'commander/tasks.db'
        db.write_bytes(b'not a sqlite database')
        result = self.run_cleanup(apply=True)
        self.assertTrue(all(p.exists() for p in self.backups))
        self.assertEqual(result['deleted'], [str(self.build)])

    def test_incomplete_new_deployment_does_not_displace_good_copies(self):
        (self.root / 'bin/backups/20261201T010101Z-incomplete').mkdir()
        self.run_cleanup(apply=True)
        self.assertTrue(all(p.exists() for p in self.backups))

    def test_symlink_build_and_nested_backup_escape(self):
        self.build.unlink()
        outside = Path(self.tmp.name) / 'outside'
        outside.write_text('preserve')
        self.build.symlink_to(outside)
        (self.backups[0] / 'escape').symlink_to(outside)
        self.run_cleanup(apply=True)
        self.assertEqual(outside.read_text(), 'preserve')
        self.assertTrue(self.backups[0].exists())
        self.assertTrue(self.build.is_symlink())

    def test_unregistered_build_never_removed(self):
        other = self.build.with_name('nexus-other')
        self.binary(other, large=True)
        self.run_cleanup(apply=True)
        self.assertTrue(other.exists())

    def test_inventory_failure_leaves_everything(self):
        def failed(root):
            raise RuntimeError('inventory unavailable')
        self.probe = failed
        with self.assertRaises(RuntimeError):
            self.run_cleanup(apply=True)
        self.assertTrue(self.build.exists())
        self.assertTrue(all(p.exists() for p in self.backups))

    def test_newly_opened_candidate_stops_and_next_run_recovers(self):
        calls = [0]
        def probe(root):
            calls[0] += 1
            return ([str(self.backups[0])] if calls[0] > 1 else [], [])
        self.probe = probe
        result = self.run_cleanup(apply=True)
        self.assertTrue(result['errors'])
        self.assertEqual(result['deleted'], [])
        self.probe = lambda root: ([], [])
        self.assertEqual(len(self.run_cleanup(apply=True)['deleted']), 3)

    def test_changed_candidate_rejected(self):
        expected = c.safe_tree(self.backups[0], self.root.stat().st_dev)
        (self.backups[0] / 'new-upload').write_text('preserve')
        with self.assertRaises(RuntimeError):
            c.remove_tree(self.backups[0], expected)
        self.assertTrue((self.backups[0] / 'new-upload').exists())

    def test_lock_contention(self):
        with (self.root / 'tools/maintenance/cleanup.lock').open('a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaises(BlockingIOError):
                self.run_cleanup(apply=True)
        self.assertTrue(self.build.exists())

    def test_invalid_registry_and_traversal(self):
        registry = self.root / 'tools/maintenance/qa-builds.json'
        for value in [{}, ['resources/../nexus-test'], ['/tmp/nexus'], ['resources/nexusrouter-development/nexus-test']]:
            registry.write_text(json.dumps(value))
            with self.assertRaises(ValueError):
                self.run_cleanup(apply=True)
        self.assertTrue(all(p.exists() for p in self.backups))

    def test_unlink_failure_is_journaled_and_replanned(self):
        with patch.object(c, 'remove_tree', side_effect=OSError('simulated unlink failure')):
            result = self.run_cleanup(apply=True)
        self.assertTrue(result['errors'])
        receipt = json.loads((self.root / 'tools/maintenance/last-cleanup.json').read_text())
        self.assertEqual(receipt['deleted'], [])
        self.assertTrue(receipt['errors'])
        self.assertEqual(len(self.run_cleanup(apply=True)['deleted']), 3)

    def test_inventory_subprocess_failure(self):
        with patch.object(c.subprocess, 'run', return_value=c.subprocess.CompletedProcess([], 1, '', 'permission denied')):
            with self.assertRaises(RuntimeError):
                c.inventory(self.root)

    def test_real_open_file_inventory(self):
        if not Path('/usr/sbin/lsof').exists():
            self.skipTest('macOS inventory executable unavailable')
        with self.build.open('rb'):
            self.assertTrue(c.used(self.build, c.inventory(self.root)))

    def test_hard_link_and_mount_boundary(self):
        other = self.build.with_name('linked')
        os.link(str(self.build), str(other))
        self.assertFalse(c.native(self.build))
        with self.assertRaises(ValueError):
            c.safe_tree(self.backups[0], -1)

    def test_symlinked_home_and_tools_rejected(self):
        alias = Path(self.tmp.name).resolve() / 'alias'
        alias.symlink_to(self.root, target_is_directory=True)
        with self.assertRaises(ValueError):
            c.run(alias, apply=True, probe=self.probe)
        target = Path(self.tmp.name).resolve() / 'foreign-tools'
        target.mkdir()
        for p in (self.root / 'tools/maintenance').iterdir():
            p.unlink()
        (self.root / 'tools/maintenance').rmdir()
        (self.root / 'tools').rmdir()
        (self.root / 'tools').symlink_to(target, target_is_directory=True)
        with self.assertRaises(ValueError):
            self.run_cleanup(apply=True)
        self.assertEqual(list(target.iterdir()), [])

    def test_uncheckpointed_retained_wal_blocks_pruning(self):
        Path(str(self.backups[-1] / 'commander/tasks.db') + '-wal').write_bytes(b'pending')
        self.run_cleanup(apply=True)
        self.assertTrue(all(p.exists() for p in self.backups))

    def test_schedule_template_is_daily_background_without_service_restart(self):
        import plistlib
        text = Path(__file__).with_name('com.nexusrouter.cleanup.plist.in').read_text()
        value = plistlib.loads(text.replace('__NEXUS_HOME__', str(self.root)).encode())
        self.assertEqual(value['StartCalendarInterval'], {'Hour': 3, 'Minute': 30})
        self.assertEqual(value['ProgramArguments'][-1], '--apply')
        self.assertNotIn('KeepAlive', value)
        self.assertNotIn('RunAtLoad', value)
        self.assertEqual(value['ProgramArguments'][0], '/usr/bin/python3')

    def test_fewer_than_three_backups_never_prunes(self):
        for p in self.backups[:3]:
            c.remove_tree(p, c.safe_tree(p, self.root.stat().st_dev))
        self.run_cleanup(apply=True)
        self.assertTrue(all(p.exists() for p in self.backups[-2:]))

    def test_scheduler_home_argument_does_not_protect_all_candidates(self):
        import plistlib
        directory = self.root.parent / 'Library/LaunchAgents'
        directory.mkdir(parents=True)
        template = Path(__file__).with_name('com.nexusrouter.cleanup.plist.in').read_text()
        value = plistlib.loads(template.replace('__NEXUS_HOME__', str(self.root)).encode())
        with (directory / 'com.nexusrouter.cleanup.plist').open('wb') as stream:
            plistlib.dump(value, stream)
        with patch.object(c.Path, 'home', return_value=self.root.parent), patch.object(c.subprocess, 'run', return_value=c.subprocess.CompletedProcess([], 0, '', '')):
            self.assertFalse(c.used(self.build, c.inventory(self.root)))


if __name__ == '__main__':
    unittest.main()
