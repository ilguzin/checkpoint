"""Installer safety checks; runtime Nginx/LiveKit checks live in native-smoke.sh."""
import io
import json
from pathlib import Path
import tarfile
import tempfile
import types
import unittest
from unittest.mock import patch


def load_setup():
    module = types.ModuleType('checkpoint_setup')
    source = Path(__file__).with_name('setup.sh').read_text().split("<<'CHECKPOINT_SETUP_PY'\n", 1)[1]
    exec(compile(source.rsplit('\nCHECKPOINT_SETUP_PY', 1)[0], 'setup.sh', 'exec'), module.__dict__)
    return module


setup = load_setup()


class SetupTests(unittest.TestCase):
    def test_certbot_without_email_and_conflicting_options(self):
        args = types.SimpleNamespace(app_domain='meet.example.com', rtc_domain='rtc.example.com',
                                     turn_domain='turn.example.com', observer_domain='status.example.com',
                                     email=None, no_email=True, public_ip='8.8.4.4', ssh_port=22)
        config = setup.settings(args)
        self.assertEqual(setup.certbot_contact(config), ['--register-unsafely-without-email'])
        args.email = 'operator@example.com'
        with self.assertRaises(RuntimeError): setup.settings(args)
        args.no_email = False
        self.assertEqual(setup.certbot_contact(setup.settings(args)), ['--email', args.email])
        args.email = None
        with self.assertRaises(RuntimeError): setup.settings(args)

    def test_archive_rejects_traversal_links_and_duplicates(self):
        for names, symlink in [(['../escape'], False), (['/escape'], False),
                               (['checkpoint'], True), (['checkpoint', 'checkpoint'], False)]:
            with self.subTest(names=names, symlink=symlink), tempfile.TemporaryDirectory() as temp:
                path = Path(temp) / 'artifact.tar.gz'
                with tarfile.open(path, 'w:gz') as archive:
                    for name in names:
                        member = tarfile.TarInfo(name)
                        if symlink:
                            member.type, member.linkname = tarfile.SYMTYPE, '/etc/passwd'
                        archive.addfile(member, io.BytesIO())
                with self.assertRaises(RuntimeError):
                    setup.archive_files(path)

    def test_bundle_detects_modified_binary(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / 'artifact.tar.gz'
            binary = b'\x7fELF\x02\x01' + b'\0' * 12 + b'\x3e\x00'
            files = {'checkpoint': binary, 'third_party/README.md': b'licenses'}
            manifest = dict(format=1, platform='linux-amd64', commit='a' * 40, version='M0B-test',
                            files={name: setup.digest(data) for name, data in files.items()})
            for corrupt in (False, True):
                with tarfile.open(path, 'w:gz') as archive:
                    for name, data in dict(files, **{'manifest.json': json.dumps(manifest).encode()}).items():
                        if corrupt and name == 'checkpoint':
                            data += b'changed'
                        member = tarfile.TarInfo(name)
                        member.size = len(data)
                        archive.addfile(member, io.BytesIO(data))
                if corrupt:
                    with self.assertRaisesRegex(RuntimeError, 'hashes'):
                        setup.bundle_files(path)
                else:
                    self.assertEqual(setup.bundle_files(path)[0]['commit'], 'a' * 40)

    def test_retry_preserves_file_and_refuses_foreign_edits_and_symlinks(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(setup, 'STATE', Path(temp) / 'state.json'):
            state = dict(files={}, links={}, secrets={'invite': 'keep-me'})
            installer = setup.Installer(state)
            path = str(Path(temp) / 'config')
            installer.write(path, 'original')
            original_inode = Path(path).stat().st_ino
            installer.write(path, 'original')
            self.assertEqual(original_inode, Path(path).stat().st_ino)
            self.assertEqual(json.loads(setup.STATE.read_text())['secrets']['invite'], 'keep-me')
            setup.verify_owned(state)
            Path(path).write_text('operator edit')
            with self.assertRaises(RuntimeError):
                installer.write(path, 'replacement')
            self.assertEqual(Path(path).read_text(), 'operator edit')
            with self.assertRaises(RuntimeError):
                setup.verify_owned(state)
            Path(path).unlink()
            Path(path).symlink_to(setup.STATE)
            with self.assertRaises(RuntimeError):
                installer.write(path, 'replacement')

    def test_setup_reentry_keeps_deployment_ssh_allowlist(self):
        with tempfile.TemporaryDirectory() as temp:
            policy = Path(temp) / 'deploy-policy.json'
            policy.write_text(json.dumps({'sshPort': 2222, 'allowedCIDRs': ['198.51.100.0/24', '2001:db8::/32']}))
            def mapped(path):
                return policy if str(path) == '/etc/checkpoint/deploy-policy.json' else Path(path)
            with patch.object(setup, 'Path', mapped):
                commands = setup.firewall_commands({'ssh_port': 2222})
                rules = '\n'.join(' '.join(command) for command in commands)
                self.assertIn('ufw allow proto tcp from 198.51.100.0/24 to any port 2222', rules)
                self.assertIn('ufw allow proto tcp from 2001:db8::/32 to any port 2222', rules)
                self.assertNotIn('ufw allow 2222/tcp', rules)
                with self.assertRaises(RuntimeError): setup.firewall_commands({'ssh_port': 22})

    def test_rejects_config_injection_and_ssh_collision(self):
        args = types.SimpleNamespace(app_domain='meet.example.com', rtc_domain='rtc.example.com',
                                     turn_domain='turn.example.com', observer_domain='status.example.com',
                                     email='operator@example.com', public_ip='8.8.4.4', ssh_port=2222)
        self.assertEqual(setup.settings(args)['ssh_port'], 2222)
        for key, value in [('app_domain', 'host;include /etc/passwd'), ('email', 'x\nKEY=value'),
                           ('public_ip', '127.0.0.1'), ('ssh_port', 443), ('rtc_domain', 'meet.example.com')]:
            changed = types.SimpleNamespace(**vars(args))
            setattr(changed, key, value)
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                setup.settings(changed)


if __name__ == '__main__':
    unittest.main()
