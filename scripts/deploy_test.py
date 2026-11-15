"""Exercise recovery file operations without root or any host service changes."""
import ast
import base64
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import Mock

source = Path(__file__).resolve().parents[1] / 'internal/deploy/harden.py'
tree = ast.parse(source.read_text())
# Load definitions only; the deployment entrypoint must never run on this host.
tree.body = [node for node in tree.body if isinstance(node, (ast.Import, ast.ImportFrom, ast.FunctionDef))]
harden = types.ModuleType('checkpoint_harden')
exec(compile(tree, str(source), 'exec'), harden.__dict__)


class RecoveryTests(unittest.TestCase):
    def test_atomic_replacement_preserves_mode_and_cleans_temporary_files(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'policy'
            harden.atomic(path, b'first', 0o600)
            harden.atomic(path, b'second', 0o644)
            self.assertEqual(path.read_bytes(), b'second')
            self.assertEqual(path.stat().st_mode & 0o777, 0o644)
            self.assertEqual(list(Path(directory).iterdir()), [path])

    def test_rollback_restores_previous_content_and_removes_only_created_file(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            original, created = root / 'original', root / 'created'
            original.write_bytes(b'changed'); created.write_bytes(b'new')
            harden.STATE = root / 'transaction.json'
            harden.STATE.write_text(json.dumps({'files': {
                str(original): {'body': base64.b64encode(b'old').decode(), 'mode': 0o600},
                str(created): None,
            }}))
            harden.run = Mock()
            harden.rollback()
            self.assertEqual(original.read_bytes(), b'old')
            self.assertEqual(original.stat().st_mode & 0o777, 0o600)
            self.assertFalse(created.exists())
            self.assertFalse(harden.STATE.exists())
            self.assertEqual(harden.run.call_args_list[0].args, ('sshd', '-t'))
            self.assertEqual(harden.run.call_args_list[-1].args, ('ufw', 'reload'))
