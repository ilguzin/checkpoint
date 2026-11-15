import unittest
import socket
import tempfile
from pathlib import Path
from unittest.mock import patch
import dev

class RouteOwnershipTest(unittest.TestCase):
    def test_active_listener_conflicts_but_recently_closed_listener_does_not(self):
        with socket.socket() as server:
            server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            server.bind(('127.0.0.1', 0))
            port = server.getsockname()[1]
            server.listen(1)
            with self.assertRaises(RuntimeError):
                dev.port_available('127.0.0.1', port)
            with socket.create_connection(('127.0.0.1', port)) as client:
                peer, _ = server.accept()
                peer.close()
                self.assertEqual(client.recv(1), b'')
        dev.port_available('127.0.0.1', port)

    def test_only_exact_non_funnel_route_is_owned(self):
        cfg = {'TCP': {'443': {'HTTPS': True}}, 'Web': {'node.ts.net:443': {'Handlers': {'/': {'Proxy': 'http://127.0.0.1:18180'}}}}}
        self.assertTrue(dev.owned_route(cfg, 'node.ts.net', 443, 18180))
        cfg['Web']['node.ts.net:443']['Handlers']['/other'] = {'Text': 'keep me'}
        self.assertFalse(dev.owned_route(cfg, 'node.ts.net', 443, 18180))
        del cfg['Web']['node.ts.net:443']['Handlers']['/other']
        cfg['AllowFunnel'] = {'node.ts.net:443': True}
        self.assertFalse(dev.owned_route(cfg, 'node.ts.net', 443, 18180))

class RecordingConfigTest(unittest.TestCase):
    def test_prepare_preserves_existing_config_and_credentials(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(dev, 'STATE', Path(tmp)):
            dev.prepare_recording()
            recorder = (Path(tmp) / 'media/recorder.token').read_bytes()
            processor = (Path(tmp) / 'media/processor.token').read_bytes()
            self.assertNotEqual(recorder, processor)
            self.assertEqual((Path(tmp) / 'media/recorder.token').stat().st_mode & 0o777, 0o600)
            config = Path(tmp) / 'media/minutes.yaml'
            config.write_text('enabled: false\n')
            dev.prepare_recording()
            self.assertEqual(config.read_text(), 'enabled: false\n')
            self.assertEqual((Path(tmp) / 'media/recorder.token').read_bytes(), recorder)
            self.assertEqual((Path(tmp) / 'media/processor.token').read_bytes(), processor)

if __name__ == '__main__':
    unittest.main()
