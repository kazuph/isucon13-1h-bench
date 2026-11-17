#!/usr/bin/env python3
"""Cross-server icon consistency and post-reboot persistence checks."""
import base64
import hashlib
import http.client
import json
import subprocess
import sys
import time
from pathlib import Path

HOSTS = ['10.99.0.11', '10.99.0.12']

def request(host, method, path, data=None, cookie=None, etag=None):
    conn = http.client.HTTPConnection(host, 8080, timeout=15)
    headers = {'Host': 'pipe.u.isucon.dev'}
    if cookie: headers['Cookie'] = cookie
    if etag: headers['If-None-Match'] = etag
    if data is not None:
        headers['Content-Type'] = 'application/json'
        data = json.dumps(data)
    conn.request(method, path, data, headers)
    response = conn.getresponse()
    result = response.status, dict((k.lower(), v) for k, v in response.getheaders()), response.read()
    conn.close()
    return result

def login(name):
    status, headers, body = request(HOSTS[1], 'POST', '/api/login', {'username': name, 'password': 'test'})
    assert status == 200, (status, body)
    return headers['set-cookie'].split(';', 1)[0]

def verify(name, expected, cookie):
    for host in HOSTS:
        status, _, body = request(host, 'GET', '/api/user/' + name, cookie=cookie)
        assert status == 200, (host, status, body)
        assert json.loads(body)['icon_hash'] == expected
        status, headers, body = request(host, 'GET', '/api/user/' + name + '/icon')
        assert status == 200 and hashlib.sha256(body).hexdigest() == expected
        assert headers['etag'] == '"' + expected + '"'
        status, _, body = request(host, 'GET', '/api/user/' + name + '/icon', etag=headers['etag'])
        assert status == 304 and body == b''

state_path = Path('logs/smoke-state.json')
if '--verify' in sys.argv:
    state = json.loads(state_path.read_text())
    verify(state['name'], state['hash'], login(state['name']))
    print('PASS: user and icon persisted across reboot; both app servers agree')
else:
    name = 'verify-' + str(int(time.time()))
    fqdn = name + '.u.isucon.dev'
    before = subprocess.check_output(['dig', '@10.99.0.11', fqdn, 'A', '+noall', '+comments'], text=True)
    assert 'NXDOMAIN' in before
    status, _, body = request(HOSTS[0], 'POST', '/api/register', {
        'name': name, 'display_name': 'Verification', 'description': '',
        'password': 'test', 'theme': {'dark_mode': False}})
    assert status == 201, (status, body)
    after = subprocess.check_output(['dig', '@10.99.0.11', fqdn, 'A', '+short'], text=True)
    assert '10.99.0.11' in after, after
    cookie = login(name)
    user = json.loads(body)
    verify(name, user['icon_hash'], cookie)
    status, _, original = request(HOSTS[0], 'GET', '/api/user/' + name + '/icon')
    assert status == 200
    updated = original + b'\nISUCON icon consistency verification\n'
    expected = hashlib.sha256(updated).hexdigest()
    status, _, body = request(HOSTS[1], 'POST', '/api/icon', {'image': base64.b64encode(updated).decode()}, cookie=cookie)
    assert status == 201, (status, body)
    verify(name, expected, cookie)
    for host in HOSTS:
        status, _, body = request(host, 'GET', '/api/user/' + name + '/icon', etag='"' + user['icon_hash'] + '"')
        assert status == 200 and hashlib.sha256(body).hexdigest() == expected
    state_path.write_text(json.dumps({'name': name, 'hash': expected}))
    print('PASS: immediate DNS registration, cross-server icon update, ETag 200/304 behavior')
