import importlib.util
import os
from pathlib import Path
import plistlib
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('home_migration', Path(__file__).with_name('migrate-nexus-home.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)

class HomeMigrationTests(unittest.TestCase):
    def test_consolidates_without_copying_skills_and_keeps_aliases(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            code = home / 'DarwinRouter'
            skills = home / 'Library/Application Support/DarwinRouter/live-test/skills'
            data = skills.parent.parent
            config = home / '.config/nexusrouter'
            for path in [code, skills, config]:
                path.mkdir(parents=True)
            (skills / 'evidence').write_bytes(b'original skills')
            inode = (skills / 'evidence').stat().st_ino
            (data / 'live-test/config.yaml').write_text('skills:\n  root: ' + str(skills) + '\n')
            agent = home / 'Library/LaunchAgents/com.darwinrouter.live-test.plist'
            agent.parent.mkdir(parents=True)
            agent.write_bytes(plistlib.dumps({'Label':'com.darwinrouter.live-test', 'ProgramArguments':['old','serve','--config',str(data / 'live-test/config.yaml')], 'EnvironmentVariables': {'DARWIN_API_TOKEN':'fixture'}}))
            binary = home / 'validated-binary'
            binary.write_bytes(b'fixture')
            binary.chmod(0o700)
            root = home / '.NexusRouter'
            moves = [(code,root/'code'),(skills,root/'skills'),(data,root/'data'),(config,root/'config')]
            with patch.object(m,'inventory',return_value=moves), patch.object(m,'check_idle'):
                m.apply(home,binary)
            self.assertEqual(inode,(root/'skills/evidence').stat().st_ino)
            self.assertTrue(skills.samefile(root/'skills'))
            self.assertTrue(code.samefile(root/'code'))
            self.assertIn(str(root/'skills'),(root/'data/live-test/config.yaml').read_text())
            service=plistlib.loads(agent.with_name('com.nexusrouter.live-test.plist').read_bytes())
            self.assertEqual(service['ProgramArguments'][0],str(root/'bin/nexus'))
            self.assertEqual(service['EnvironmentVariables'],{'NEXUS_API_TOKEN':'fixture'})
            with self.assertRaises(RuntimeError):m.prepare(home)

    def test_longest_path_first(self):
        moves=[(Path('/old'),Path('/new/data')),(Path('/old/skills'),Path('/new/skills'))]
        self.assertEqual(m.rewrite('/old/skills/file',moves),'/new/skills/file')

if __name__ == '__main__':unittest.main()
