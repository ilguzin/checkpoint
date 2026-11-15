"""Publication checks must reject private artifacts without disclosing their contents."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('public_content', Path(__file__).with_name('public-content-check.py'))
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


class PublicContentTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)

    def write(self, name, content=''):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
        return name

    def test_rejects_local_files_even_when_force_added(self):
        names = [self.write(name) for name in ('.dev/access.json', '.env', '.env.production',
                 'docs/plans/release.md', 'docs/reviews/report.md', 'history.bundle',
                 'deploy/targets/server.local.yaml', 'checkpoint.db', 'signing.key')]
        self.assertEqual(len(checker.audit(self.root, names)), len(names))

    def test_example_environment_and_public_key_are_allowed(self):
        names = [self.write('.env.example', 'SECRET=generated-at-setup'), self.write('release.pub', 'public key')]
        self.assertEqual(checker.audit(self.root, names), [])

    def test_secret_markers_in_source_are_redacted(self):
        token = 'gh' + 'p_' + 'A' * 36
        pem = '-----BEGIN ' + 'OPENSSH ' + 'PRIVATE KEY-----'
        names = [self.write('src/config.ts', token), self.write('sample.txt', pem)]
        errors = checker.audit(self.root, names)
        self.assertEqual(len(errors), 2)
        self.assertNotIn(token, '\n'.join(errors))
        self.assertNotIn(pem, '\n'.join(errors))

    def test_markdown_images_and_regular_links_are_checked(self):
        self.write('docs/screen image.svg', '<svg/>')
        self.write('LICENSE', 'license')
        name = self.write('README.md', '[License](LICENSE) ![UI](<docs/screen image.svg>) [Remote](https://example.com)')
        self.assertEqual(checker.audit(self.root, [name]), [])
        self.write(name, '![Missing](docs/missing.png)')
        self.assertEqual(len(checker.audit(self.root, [name])), 1)

    def test_symlinks_and_markdown_cannot_escape_repository(self):
        (self.root / 'outside').symlink_to(self.root.parent, target_is_directory=True)
        name = self.write('README.md', '[Outside](../)')
        errors = checker.audit(self.root, ['outside', name])
        self.assertEqual(len(errors), 2)

    def test_concrete_private_infrastructure_is_rejected(self):
        address = 'tail' + 'abcdef' + '.ts.net'
        name = self.write('docs/server.md', 'https://node.' + address)
        self.assertEqual(len(checker.audit(self.root, [name])), 1)


if __name__ == '__main__':
    unittest.main()
