import ast
import importlib.util
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('checkpoint_release', Path(__file__).with_name('release.py'))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


class ReleaseTests(unittest.TestCase):
    def test_manifest_and_asset_validation(self):
        asset = dict(name='checkpoint-linux-amd64.tar.gz', os='linux', arch='amd64', size=1, sha256='a' * 64, kind='checkpoint')
        self.assertEqual(release.manifest_for('v0.1.0', 1, '1.13.11', 2, [asset])['sequence'], 1)
        for name in ['../checkpoint', 'payload.sh', 'a/b', 'release.json']:
            with self.assertRaises(ValueError):
                release.asset_kind(name)
        for tag in ['v1.0.0-rc1', '$(secret)', 'v01.0.0']:
            with self.assertRaises(ValueError):
                release.manifest_for(tag, 1, '1.13.11', 2, [asset])

    def test_existing_key_is_retained_and_partial_pair_recovers(self):
        with tempfile.TemporaryDirectory() as temp:
            secret, public = Path(temp) / 'release.key', Path(temp) / 'release.pub'
            secret.write_text('fixture encrypted key'); secret.chmod(0o600); public.write_text('fixture public key')
            with patch.object(release, 'key_paths', return_value=(secret, public)), patch.object(release.shutil, 'which', return_value='/minisign'), patch.object(release, 'run') as run:
                release.keygen()
                run.assert_not_called()
                public.unlink()
                release.keygen()
                self.assertEqual(run.call_args.args[:2], ('minisign', '-R'))
                self.assertEqual(secret.read_text(), 'fixture encrypted key')
                release.keygen(force=True)
                self.assertIn('-f', run.call_args.args)

    def test_private_key_path_cannot_alias_public_path(self):
        with patch.dict(release.os.environ, {'CHECKPOINT_SIGNING_KEY_FILE': '/tmp/test-release.pub'}):
            with self.assertRaisesRegex(ValueError, 'must not end in .pub'):
                release.key_paths()

    def test_sign_cli_uses_checkout_schema_and_accepts_explicit_override(self):
        with tempfile.TemporaryDirectory() as temp:
            source = Path(temp) / 'store.go'
            source.write_text('package core\nconst SchemaVersion = 7\n')
            argv = ['release.py', 'sign', 'v0.1.0', '--sequence', '1']
            with patch.object(release, 'SCHEMA_SOURCE', source), patch.object(release, 'sign') as sign, patch.object(release.os, 'umask'), patch('sys.argv', argv):
                release.main()
                self.assertEqual(sign.call_args.args[0].schema, 7)
            with patch.object(release, 'SCHEMA_SOURCE', source), patch.object(release, 'sign') as sign, patch.object(release.os, 'umask'), patch('sys.argv', [*argv, '--schema', '2']):
                release.main()
                self.assertEqual(sign.call_args.args[0].schema, 2)

    def test_schema_source_fails_closed_on_missing_or_ambiguous_constant(self):
        with tempfile.TemporaryDirectory() as temp:
            source = Path(temp) / 'store.go'
            for body in ['package core\n', 'const SchemaVersion = 0\n', 'const SchemaVersion = 3\nconst SchemaVersion = 4\n']:
                source.write_text(body)
                with patch.object(release, 'SCHEMA_SOURCE', source), self.assertRaisesRegex(ValueError, 'Cannot determine SchemaVersion'):
                    release.source_schema()

    def test_rendered_installer_uses_same_checkout_schema_and_rejects_other_targets(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            source, key, output = root / 'store.go', root / 'release.pub', root / 'install.sh'
            source.write_text('package core\nconst SchemaVersion = 7\n')
            key.write_text('untrusted comment: test key\n' + 'A' * 56 + '\n')
            argv = ['release.py', 'installer', '--public-key', str(key), '--output', str(output)]
            with patch.object(release, 'SCHEMA_SOURCE', source), patch.object(release.os, 'umask'), patch('sys.argv', argv):
                release.main()
            rendered = output.read_text()
            self.assertNotIn('__CHECKPOINT_SCHEMA__', rendered)
            self.assertNotIn('__CHECKPOINT_PUBLIC_KEY__', rendered)
            python = rendered.split("<<'CHECKPOINT_INSTALL_PY'\n", 1)[1].rsplit('CHECKPOINT_INSTALL_PY', 1)[0]
            tree = ast.parse(python)
            schema_node = next(node for node in tree.body if isinstance(node, ast.Assign) and any(isinstance(target, ast.Name) and target.id == 'SCHEMA' for target in node.targets))
            schema = ast.literal_eval(schema_node.value)
            self.assertEqual(schema, '7')
            compatible = next(node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == 'compatible_schema')
            namespace = {'SCHEMA': schema}
            exec(compile(ast.Module(body=[compatible], type_ignores=[]), '<installer schema guard>', 'exec'), namespace)
            guard = namespace['compatible_schema']
            self.assertTrue(guard({'min': 7, 'max': 7, 'target': 7}))
            for value in [None, {}, {'min': 6, 'max': 6, 'target': 6}, {'min': 8, 'max': 8, 'target': 8}, {'min': 6, 'max': 8, 'target': 8}, {'min': True, 'max': 7, 'target': 7}, {'min': 8, 'max': 7, 'target': 7}]:
                self.assertFalse(guard(value), value)

class ResumeTests(unittest.TestCase):
    def test_partial_signature_upload_resumes_without_replacing_manifest(self):
        import argparse, hashlib, json
        with tempfile.TemporaryDirectory() as temporary:
            root=Path(temporary);secret=root/'secret.key';public=root/'public.pub';secret.write_text('test-only');public.write_text('A'*56)
            script=Path(release.__file__).with_name('install.sh').read_text().replace('__CHECKPOINT_PUBLIC_KEY__','A'*56).replace('__CHECKPOINT_SCHEMA__','5').encode()
            base={'checkpoint-linux-amd64.tar.gz':b'test-bundle','install.sh':script}
            entries=[dict(name=n,kind=release.asset_kind(n),os='linux',arch='amd64',size=len(b),sha256=hashlib.sha256(b).hexdigest()) for n,b in sorted(base.items())]
            manifest=json.dumps(release.manifest_for('v0.1.0',1,'1.13.11',5,entries)).encode()
            args=argparse.Namespace(repo=release.REPOSITORY,tag='v0.1.0',sequence=1,livekit_version='1.13.11',schema=5)
            for stage in ['none','manifest','complete']:
                bodies=dict(base)
                if stage!='none':bodies['release.json']=manifest
                if stage=='complete':bodies['release.json.minisig']=b'existing-signature'
                metadata=dict(id=1,draft=True,assets=[dict(id=i,name=n,size=len(b),updated_at='now') for i,(n,b) in enumerate(bodies.items())])
                calls=[]
                def run(*command,**kwargs):
                    calls.append(command)
                    if command[:2]==('minisign','-S'):Path(command[command.index('-x')+1]).write_bytes(b'new-signature')
                with patch.object(release,'keygen',return_value=(secret,public)),patch.object(release,'release_metadata',return_value=metadata),patch.object(release,'download_asset',side_effect=lambda repo,asset,path:path.write_bytes(bodies[asset['name']])),patch.object(release,'run',side_effect=run):release.sign(args)
                uploads=[c for c in calls if c[:3]==('gh','release','upload')]
                self.assertEqual(len(uploads),0 if stage=='complete' else 1)
                if stage=='manifest':self.assertEqual([Path(x).name for x in uploads[0][4:-2]],['release.json.minisig'])
                self.assertEqual(sum(c[:2]==('minisign','-S') for c in calls),int(stage!='complete'))
