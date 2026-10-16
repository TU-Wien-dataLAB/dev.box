#!/usr/bin/env python3
"""Build auth-server and verify the chart gateway's SSH banners. No cluster access.

Requires Docker, Helm, Python 3 and OpenSSH. CONTAINERSSH_IMAGE overrides the
rendered chart default; STUB_IMAGE overrides python:3.12-alpine.
"""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]


def run(*args):
    return subprocess.check_output(args, cwd=ROOT, text=True).strip()


FAKE_AUTHENTIK = r'''
import json
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlparse

users = json.load(open('/fixtures/users.json'))
class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        url = urlparse(self.path)
        if url.path != '/api/v3/core/users/':
            self.send_error(404)
            return
        if self.headers.get('Authorization') != 'Bearer fixture-token':
            self.send_error(403)
            return
        query = parse_qs(url.query)
        matches = users
        if 'attributes' in query:
            attributes = json.loads(query['attributes'][0])
            matches = [u for u in matches if u['attributes'] == attributes]
        if 'username' in query:
            matches = [u for u in matches if u['username'] == query['username'][0]]
        if 'groups_by_name' in query:
            matches = [u for u in matches if query['groups_by_name'][0] in u['groups']]
        body = json.dumps({'pagination': {'count': len(matches)}, 'results': matches}).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *_):
        pass
HTTPServer(('0.0.0.0', 8080), Handler).serve_forever()
'''


def main():
    rendered = run('helm', 'template', 'banner-test', 'charts/containerssh',
                   '--show-only', 'templates/deployment.yaml',
                   '--set', 'kata.enabled=false', '--set', 'kubernetes.mode=connection',
                   '--set', 'auth.publicKey.webhook.url=http://auth:8080')
    default_image = re.search(r'^\s+image: "([^"]+)"', rendered, re.MULTILINE).group(1)
    gateway_image = os.environ.get('CONTAINERSSH_IMAGE', default_image)
    suffix = uuid.uuid4().hex[:12]
    network = 'devbox-auth-messages-' + suffix
    auth_image = 'devbox-auth-messages:' + suffix
    containers = []
    network_created = False
    image_built = False

    def container(*args):
        cid = run('docker', 'run', '-d', '--network', network, *args)
        containers.append(cid)
        return cid

    def host_port(cid, port):
        return run('docker', 'port', cid, str(port) + '/tcp').rsplit(':', 1)[1]

    try:
        subprocess.run(['docker', 'build', '-t', auth_image, 'auth-server'], cwd=ROOT, check=True)
        image_built = True
        run('docker', 'network', 'create', network)
        network_created = True
        with tempfile.TemporaryDirectory(prefix='devbox-auth-messages-') as directory:
            tmp = Path(directory)
            keys = {}
            for name in ('alice', 'inactive', 'duplicate', 'nonmember', 'unknown'):
                key = tmp / name
                run('ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(key))
                keys[name] = ' '.join(key.with_suffix('.pub').read_text().split()[:2])
            users = [
                {'username': name, 'is_active': active,
                 'attributes': {'sshPublicKey': [keys[key]]}, 'groups': groups}
                for name, key, active, groups in (
                    ('alice', 'alice', True, ['ssh-users']),
                    ('inactive', 'inactive', False, []),
                    ('duplicate-1', 'duplicate', True, []),
                    ('duplicate-2', 'duplicate', True, []),
                    ('nonmember', 'nonmember', True, []),
                )
            ]
            (tmp / 'users.json').write_text(json.dumps(users))
            (tmp / 'authentik.py').write_text(FAKE_AUTHENTIK)
            fake = container('--network-alias', 'authentik', '-v', f'{tmp}:/fixtures:ro',
                             os.environ.get('STUB_IMAGE', 'python:3.12-alpine'),
                             'python3', '/fixtures/authentik.py')
            auth = container('--network-alias', 'auth', '-p', '127.0.0.1::8080',
                             '-e', 'AUTHENTIK_URL=http://authentik:8080',
                             '-e', 'AUTHENTIK_TOKEN=fixture-token',
                             '-e', 'AUTH_SERVER_ALLOWED_TEMPLATES=["ubuntu"]',
                             '-e', 'AUTH_SERVER_REQUIRE_GROUP=ssh-users', auth_image)
            auth_url = 'http://127.0.0.1:' + host_port(auth, 8080)

            def post(endpoint, body):
                request = urllib.request.Request(auth_url + '/' + endpoint,
                                                 json.dumps(body).encode(),
                                                 {'Content-Type': 'application/json'})
                try:
                    response = urllib.request.urlopen(request, timeout=3)
                except urllib.error.HTTPError as error:
                    response = error
                with response:
                    return response.status, json.load(response)

            deadline = time.monotonic() + 30
            while True:
                try:
                    status, body = post('pubkey', {'username': 'ubuntu', 'publicKey': keys['alice']})
                    if status == 200 and body.get('success'):
                        break
                except (OSError, ValueError):
                    pass
                if time.monotonic() >= deadline:
                    raise AssertionError('auth-server/fake authentik did not become ready')
                time.sleep(0.2)
            assert body['authenticatedUsername'] == 'alice' and 'message' not in body, body
            status, body = post('authz', {'username': 'ubuntu', 'authenticatedUsername': 'alice'})
            assert status == 200 and body['success'] and 'message' not in body, (status, body)
            print('PASS: built auth image authenticates and authorizes the verified owner', flush=True)

            (tmp / 'gateway.yaml').write_text('''ssh:
  listen: 0.0.0.0:2222
auth:
  publicKey:
    method: webhook
    webhook:
      url: http://auth:8080
      timeout: 2s
      authTimeout: 3s
  authz:
    method: webhook
    webhook:
      url: http://auth:8080
      timeout: 2s
      authTimeout: 3s
log:
  level: 6
''')
            gateway = container('-p', '127.0.0.1::2222',
                                '-v', f'{tmp / "gateway.yaml"}:/etc/containerssh/config.yaml:ro',
                                gateway_image, '--config', '/etc/containerssh/config.yaml')
            port = host_port(gateway, 2222)
            deadline = time.monotonic() + 30
            while 'SSH server running' not in run('docker', 'logs', gateway):
                if time.monotonic() >= deadline:
                    raise AssertionError('gateway did not become ready')
                time.sleep(0.2)
            print('Gateway: ' + gateway_image, flush=True)
            for username, key, message in (
                ('unknown-template', 'alice', "no pod template matches SSH username 'unknown-template'"),
                ('ubuntu', 'unknown', 'public key is not enrolled for any user'),
                ('ubuntu', 'duplicate', 'public key is not enrolled for any user'),
                ('ubuntu', 'inactive', 'the account owning this key is inactive'),
                ('ubuntu', 'nonmember', "user 'nonmember' is not a member of the required group 'ssh-users'"),
            ):
                started = time.monotonic()
                result = subprocess.run([
                    'ssh', '-F', '/dev/null', '-i', str(tmp / key),
                    '-o', 'IdentitiesOnly=yes', '-o', 'BatchMode=yes',
                    '-o', 'PreferredAuthentications=publickey',
                    '-o', 'StrictHostKeyChecking=no', '-o', 'UserKnownHostsFile=/dev/null',
                    '-o', 'ConnectTimeout=5', '-p', port, username + '@127.0.0.1', 'true',
                ], capture_output=True, text=True, timeout=10)
                output = result.stdout + result.stderr
                assert result.returncode == 255, output
                assert message in output and 'Permission denied' in output, output
                assert output.index(message) < output.index('Permission denied'), output
                assert time.monotonic() - started < 5, 'denial was not prompt'
                print(f'PASS: SSH {key}/{username}: {message}', flush=True)

            status, body = post('password', {'username': 'ubuntu', 'passwordBase64': 'cGFzcw=='})
            assert status == 200 and not body['success'] and 'not supported' in body['message'], body
            run('docker', 'stop', fake)
            for endpoint, request in (
                ('pubkey', {'username': 'ubuntu', 'publicKey': keys['alice']}),
                ('authz', {'username': 'ubuntu', 'authenticatedUsername': 'alice'}),
            ):
                status, body = post(endpoint, request)
                assert status == 500 and not body['success'] and 'message' not in body, (status, body)
            print('PASS: password denial and fail-closed pubkey/authz outages without messages', flush=True)
    except Exception:
        for cid in containers:
            subprocess.run(['docker', 'logs', '--tail', '40', cid], check=False)
        raise
    finally:
        for cid in reversed(containers):
            subprocess.run(['docker', 'rm', '-f', cid], stdout=subprocess.DEVNULL, check=False)
        if network_created:
            subprocess.run(['docker', 'network', 'rm', network], stdout=subprocess.DEVNULL, check=False)
        if image_built:
            subprocess.run(['docker', 'image', 'rm', auth_image], stdout=subprocess.DEVNULL, check=False)


if __name__ == '__main__':
    main()
