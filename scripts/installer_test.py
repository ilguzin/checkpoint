"""Real bootstrap and Minisign; only setup effects/network use isolated fixtures."""
import ast, hashlib, io, json, os, shutil, subprocess, tarfile, tempfile, unittest
from pathlib import Path, PosixPath
from unittest.mock import patch
TEMPLATE=Path(__file__).with_name('install.sh')
FLAGS='--public-ip --app-domain --rtc-domain --turn-domain --observer-domain --email --no-email --ssh-port --bundle --apply'

@unittest.skipUnless(shutil.which('minisign'),'Run scripts/installer-smoke.sh for real signatures')
class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
        self.root=Path(self.temp.name);self.release=self.root/'release';self.release.mkdir()
        self.private=self.root/'test.key';public=self.root/'test.pub'
        subprocess.run(['minisign','-G','-W','-s',str(self.private),'-p',str(public)],check=True,capture_output=True)
        self.key=public.read_text().strip().splitlines()[-1]
        self.installer=self.root/'install.sh';self.installer.write_text(TEMPLATE.read_text().replace('__CHECKPOINT_PUBLIC_KEY__',self.key).replace('__CHECKPOINT_SCHEMA__','5'))
        self.files={'checkpoint':f'#!/bin/sh\nprintf "%s\\n" "{self.key}"\n'.encode(),'setup.sh':f'''#!/usr/bin/env python3
import json,os,sys
from pathlib import Path
if '--help' in sys.argv: print({FLAGS!r})
else:
    root=Path(os.environ['CHECKPOINT_FIXTURE_STATE']);root.mkdir(exist_ok=True)
    (root/'setup.json').write_text(json.dumps(sys.argv[1:]))
'''.encode()}
        self.pack();self.sign()
    def pack(self):
        internal=dict(format=1,platform='linux-amd64',commit='a'*40,version='v0.1.0',files={n:hashlib.sha256(b).hexdigest() for n,b in self.files.items()})
        with tarfile.open(self.release/'checkpoint-linux-amd64.tar.gz','w:gz') as tar:
            for name,data in {**self.files,'manifest.json':json.dumps(internal).encode()}.items():
                member=tarfile.TarInfo(name);member.size=len(data);member.mode=0o755;tar.addfile(member,io.BytesIO(data))
    def sign(self):
        body=(self.release/'checkpoint-linux-amd64.tar.gz').read_bytes()
        self.manifest=dict(format=1,project='checkpoint',repository='ilguzin/checkpoint',channel='stable',sequence=1,version='v0.1.0',livekitVersion='1.13.11',schema=dict(min=5,max=5,target=5),assets=[dict(name='checkpoint-linux-amd64.tar.gz',kind='checkpoint',os='linux',arch='amd64',size=len(body),sha256=hashlib.sha256(body).hexdigest())]);self.resign()
    def resign(self):
        path=self.release/'release.json';path.write_text(json.dumps(self.manifest))
        subprocess.run(['minisign','-S','-s',str(self.private),'-m',str(path),'-x',str(self.release/'release.json.minisig')],check=True,capture_output=True)
    def verify(self):
        return subprocess.run(['bash',str(self.installer),'--verify-only','--release-dir',str(self.release),'--version','v0.1.0'],text=True,capture_output=True)
    def test_signed_bundle(self):
        result=self.verify();self.assertEqual(result.returncode,0,result.stderr);self.assertIn('No installation performed',result.stdout)
    def test_tampering_signature_hash_key(self):
        path=self.release/'release.json';saved=path.read_bytes();path.write_bytes(saved+b' ')
        self.assertNotEqual(self.verify().returncode,0);path.write_bytes(saved)
        path=self.release/'checkpoint-linux-amd64.tar.gz';saved=path.read_bytes();path.write_bytes(saved[:-1]+bytes([saved[-1]^1]))
        self.assertIn('hash mismatch',self.verify().stderr);path.write_bytes(saved)
        self.files['checkpoint']=b'#!/bin/sh\necho wrong-key\n';self.pack();self.sign()
        self.assertIn('trust keys differ',self.verify().stderr)
    def test_wrong_schema_and_missing_setup_flags(self):
        self.manifest['schema']=dict(min=6,max=6,target=6);self.resign();self.assertIn('Unsupported schema',self.verify().stderr)
        self.files['setup.sh']=b'#!/bin/sh\necho unsupported\n';self.pack();self.sign();self.assertIn('does not support',self.verify().stderr)
    def test_local_draft_cannot_install(self):
        result=subprocess.run(['bash',str(self.installer),'--release-dir',str(self.release),'--apply'],capture_output=True,text=True)
        self.assertNotEqual(result.returncode,0);self.assertIn('only with --verify-only',result.stderr)
    def source(self):return self.installer.read_text().split("<<'CHECKPOINT_INSTALL_PY'\n",1)[1].rsplit('\nCHECKPOINT_INSTALL_PY',1)[0]
    def test_setup_handoff_and_update_trust(self):
        state=self.root/'state';state.mkdir();setup_state=self.root/'fixture'
        requested=[]
        def fetch(url,timeout=60):
            requested.append(url)
            if url=='https://api.github.com/repos/ilguzin/checkpoint/releases/latest':return io.BytesIO(b'{"tag_name":"v0.1.0","draft":false,"prerelease":false}')
            prefix='https://github.com/ilguzin/checkpoint/releases/download/v0.1.0/'
            self.assertTrue(url.startswith(prefix),url);return io.BytesIO((self.release/url[len(prefix):]).read_bytes())
        argv=['installer','--public-ip','8.8.4.4','--app-domain','meet.example.com','--rtc-domain','rtc.example.com','--turn-domain','turn.example.com','--observer-domain','status.example.com','--no-email','--apply']
        with patch('sys.argv',argv),patch('urllib.request.urlopen',side_effect=fetch),patch.dict(os.environ,{'CHECKPOINT_FIXTURE_STATE':str(setup_state)}):
            source=self.source().replace("Path('/etc/checkpoint/", "Path('"+str(state)+"/")
            exec(compile(source,'installer','exec'),{'__name__':'__main__'})
        self.assertIn('--apply',json.loads((setup_state/'setup.json').read_text()))
        self.assertEqual((state/'release.pub').read_text().strip(),self.key)
        accepted=json.loads((state/'update-state.json').read_text());self.assertEqual(accepted['sequence'],1)
        self.assertEqual(accepted['digest'],hashlib.sha256((self.release/'release.json').read_bytes()).hexdigest())
        self.assertTrue(requested[0].endswith('/releases/latest'))
    def test_wizard_preflight_sslip_without_email(self):
        declarations=[node for node in ast.parse(self.source()).body if isinstance(node,(ast.Import,ast.ImportFrom,ast.FunctionDef))]
        namespace={};exec(compile(ast.Module(body=declarations,type_ignores=[]),'wizard','exec'),namespace)
        class Terminal:
            answers=iter(['8.8.4.4\n']+['\n']*8)
            def __enter__(self):return self
            def __exit__(self,*args):return False
            def write(self,value):pass
            def flush(self):pass
            def readline(self):return next(self.answers)
        with patch('sys.argv',['install.sh']):options=namespace['arguments']()
        with patch('builtins.open',return_value=Terminal()):namespace['wizard'](options)
        flags=namespace['setup_arguments'](options)
        self.assertIn('8.8.4.4.sslip.io',flags);self.assertIn('status.8.8.4.4.sslip.io',flags);self.assertIn('--no-email',flags);self.assertNotIn('--apply',flags)
