#!/usr/bin/env python3
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import ssl
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request

credentials = json.load(sys.stdin)
assert credentials['identity_match'] is True
assert credentials['authority'] == '10.10.1.66:30003'
assert credentials['project'] == 'ani-resource-factor10-20261022'
os.umask(0o077)
directory = Path(tempfile.mkdtemp(prefix='registry-preflight-', dir='/home/chabking/tenantenv-20261101/images-d807-cymq2rk8'))
result = {'read_only': True, 'public_source': credentials['public_source'], 'probes': []}
try:
    ca = directory / 'ca.crt'
    ca.write_bytes(base64.b64decode(credentials['ca_pem_b64']))
    auth = directory / 'auth.json'
    encoded = base64.b64encode((credentials['username'] + ':' + credentials['password']).encode()).decode()
    auth.write_text(json.dumps({'auths': {credentials['authority']: {'auth': encoded}}}))
    auth.chmod(0o600)
    context = ssl.create_default_context(cafile=str(ca))
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=context))
    for path in ['/v2/', '/api/v2.0/projects/ani-resource-factor10-20261022']:
        request = urllib.request.Request('https://' + credentials['authority'] + path, headers={'Authorization': 'Basic ' + encoded})
        item = {'path': path}
        try:
            with opener.open(request, timeout=15) as response:
                raw = response.read(65536)
                item['status'] = response.status
                if path.startswith('/api/'):
                    data = json.loads(raw)
                    item['public_fields'] = {k: data.get(k) for k in ['project_id', 'name', 'owner_id']}
        except urllib.error.HTTPError as error:
            item['status'] = error.code
            challenge = error.headers.get('WWW-Authenticate', '')
            item['auth_realm'] = re.search(r'realm="([^"]+)"', challenge).group(1) if 'realm="' in challenge else None
        except Exception as error:
            item['error_class'] = type(error).__name__
            item['tls_error'] = isinstance(getattr(error, 'reason', None), ssl.SSLError)
        result['probes'].append(item)
    image = 'docker://10.10.1.66:30003/ani-resource-factor10-20261022/ani-modeldev-step:tenantenv-d807cbdbd5dd-cymq2rk8'
    probe = subprocess.run(['skopeo', 'inspect', '--authfile', str(auth), '--cert-dir', str(directory), '--tls-verify=true', '--raw', image], capture_output=True, timeout=40)
    message = probe.stderr.decode(errors='replace')
    flags = ['manifest unknown', 'name unknown', 'unauthorized', 'denied', 'x509', 'certificate', 'connection refused', 'timeout', 'not found', '404', '403', '400', '502', 'getting credentials', 'http response']
    redacted = message.replace(credentials['password'], '<redacted-password>').replace(encoded, '<redacted-auth>').replace(credentials['username'], '<redacted-principal>')
    redacted = re.sub(r'(?i)(token|access_token|password|authorization)([=: ]+)[^\s&,]+', r'\1\2<redacted>', redacted)
    result['skopeo'] = {'exit': probe.returncode, 'stderr_sha256': hashlib.sha256(probe.stderr).hexdigest(), 'error_flags': [flag for flag in flags if flag in message.lower()], 'redacted_error': redacted[:1500], 'manifest_present': bool(probe.stdout)}
finally:
    shutil.rmtree(directory)
    result['temporary_auth_and_ca_removed'] = not directory.exists()
print(json.dumps(result))
