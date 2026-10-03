import importlib.util
import os
from pathlib import Path
import plistlib
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('migration', Path(__file__).with_name('migrate-macos-name.py'))
migration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(migration)


class MigrationTests(unittest.TestCase):
    def fixture(self, home):
        old, new, agent, target = migration.paths(home)
        (old / 'live-test/skills').mkdir(parents=True)
        (old / 'live-test/skills/retained').write_bytes(b'skill evidence')
        (old / 'live-test/config.yaml').write_text('skills:\n  root: ' + str(old / 'live-test/skills') + '\n')
        agent.parent.mkdir(parents=True)
        agent.write_bytes(plistlib.dumps({'Label': 'com.darwinrouter.live-test',
            'ProgramArguments': ['/legacy/bin/darwin', 'serve', '--config', str(old / 'live-test/config.yaml')],
            'EnvironmentVariables': {'DARWIN_API_TOKEN': 'private-fixture'}}))
        return old, new, agent, target

    def test_preserves_data_and_legacy_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            old, new, agent, target = self.fixture(home)
            inode = (old / 'live-test/skills/retained').stat().st_ino
            with patch.object(migration, 'require_idle') as idle:
                migration.migrate(home)
                idle.assert_called_once_with(old)
            self.assertTrue(old.samefile(new))
            self.assertEqual(inode, (new / 'live-test/skills/retained').stat().st_ino)
            self.assertIn(str(new), (new / 'live-test/config.yaml').read_text())
            self.assertEqual(0o600, (new / 'live-test/config.yaml').stat().st_mode & 0o777)
            data = plistlib.loads(target.read_bytes())
            self.assertEqual(data['Label'], 'com.nexusrouter.live-test')
            self.assertEqual(data['EnvironmentVariables'], {'NEXUS_API_TOKEN': 'private-fixture'})
            self.assertFalse(agent.exists())
            self.assertTrue((new / 'name-migration-backup/launch-agent.plist').exists())

    def test_busy_or_conflicting_destination_never_moves_data(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            old, new, agent, target = self.fixture(home)
            with patch.object(migration, 'require_idle', side_effect=RuntimeError('busy')):
                with self.assertRaises(RuntimeError):
                    migration.migrate(home)
            self.assertTrue(old.is_dir())
            self.assertFalse(new.exists())
            new.mkdir()
            with self.assertRaises(RuntimeError):
                migration.prepare(home)
            self.assertTrue(agent.exists())


if __name__ == '__main__':
    unittest.main()
