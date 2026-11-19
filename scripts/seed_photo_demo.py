#!/usr/bin/env python3
"""Prepare ten portrait or landscape stock PNGs and seed one explicitly labelled local test device."""
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
from pathlib import Path
import base64
import getpass
import hashlib
import http.cookiejar
import json
import os
import struct
import urllib.parse
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
PORTRAIT = os.environ.get('PHOTO_DEMO_ORIENTATION', 'portrait') == 'portrait'
WIDTH, HEIGHT = (900, 1600) if PORTRAIT else (1600, 900)
RATIO = '9:16' if PORTRAIT else '16:9'
OUTPUT = ROOT / ('build/photo-demo-portrait' if PORTRAIT else 'build/photo-demo')
SOURCES = [
    ('1576956046184-f0d8c0d25b82', 'https://unsplash.com/photos/jbGDLTF5nFs'),
    ('1550947819-98400828e3e0', 'https://unsplash.com/photos/elMz_DEwavA'),
    ('1559416981-2d2d008e1c26', 'https://unsplash.com/photos/HMT5XIUctUE'),
    ('1595446886415-4bcf8fefebcc', 'https://unsplash.com/photos/xMUgcswXdeQ'),
    ('1731512820214-901f23bb6615', 'https://unsplash.com/photos/YyNI6FCitt8'),
    ('1702360373582-968d932ac789', 'https://unsplash.com/photos/cpXJYdJSNeM'),
    ('1641700038318-fbd7f0a64a7d', 'https://unsplash.com/photos/IwgODdGlgWY'),
    ('1561175024-ef321892a9a9', 'https://unsplash.com/photos/l9M1q8W-pyA'),
    ('1716872491368-cbef5a6b0532', 'https://unsplash.com/'),
    ('1516291251364-8e3c644a43aa', 'https://unsplash.com/collections/8934879/cityscape'),
]


def prepare(item):
    index, (photo, source) = item
    path = OUTPUT / f'photo-{index:02}.png'
    url = f'https://images.unsplash.com/photo-{photo}?fm=png&fit=crop&w={WIDTH}&h={HEIGHT}'
    if not path.exists():
        with urllib.request.urlopen(url, timeout=60) as response:
            data = response.read(8 * 1024 * 1024 + 1)
        if not data.startswith(b'\x89PNG\r\n\x1a\n') or struct.unpack('>II', data[16:24]) != (WIDTH, HEIGHT):
            raise ValueError(f'Photo {index}: expected a {WIDTH} × {HEIGHT} PNG')
        if len(data) > 8 * 1024 * 1024:
            raise ValueError(f'Photo {index}: PNG exceeds upload limit')
        path.write_bytes(data)
    data = path.read_bytes()
    assert data.startswith(b'\x89PNG\r\n\x1a\n') and struct.unpack('>II', data[16:24]) == (WIDTH, HEIGHT)
    return {'index':index, 'file':path.name, 'source':source, 'download':url, 'width':WIDTH, 'height':HEIGHT,
            'sha256':hashlib.sha256(data).hexdigest(), 'bytes':len(data)}


def main():
    OUTPUT.mkdir(parents=True, exist_ok=True)
    with ThreadPoolExecutor(max_workers=4) as workers:
        photos = list(workers.map(prepare, enumerate(SOURCES, 1)))
    (OUTPUT / 'sources.json').write_text(json.dumps(photos, ensure_ascii=False, indent=2) + '\n')
    if os.environ.get('PHOTO_DEMO_DOWNLOAD_ONLY') == '1':
        print(f'Prepared ten distinct {WIDTH} × {HEIGHT} PNG photos')
        return
    # This workflow deliberately accepts loopback HTTP only; it cannot seed a production server.
    url = os.environ.get('PHOTO_DEMO_URL', 'http://127.0.0.1:8080').rstrip('/')
    parsed = urllib.parse.urlparse(url)
    if parsed.scheme != 'http' or parsed.hostname not in ('127.0.0.1', 'localhost') or parsed.username or parsed.password:
        raise ValueError('Photo demo seeding is restricted to a local HTTP test server')
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
    csrf = ''

    def api(method, path, body=None, token=None, content_type=None):
        headers = {'X-CSRF-Token':csrf}
        if token:
            headers['Authorization'] = 'Bearer ' + token
        if content_type:
            headers['Content-Type'] = content_type
            data = body
        else:
            headers['Content-Type'] = 'application/json'
            data = json.dumps(body).encode() if body is not None else None
        with opener.open(urllib.request.Request(url + '/api/v1' + path, data=data, method=method, headers=headers), timeout=30) as response:
            return json.load(response)

    password = os.environ.get('PHOTO_DEMO_PASSWORD') or getpass.getpass('Local admin password: ')
    auth = api('POST', '/auth/login', {'username':os.environ.get('PHOTO_DEMO_USERNAME', 'reviewadmin'), 'password':password})
    csrf = auth['csrf_token']
    state_path = OUTPUT / 'device-private.json'
    if state_path.exists():
        state = json.loads(state_path.read_text())
    else:
        state = {'installation_id':'photo-demo-' + uuid.uuid4().hex, 'registration_secret':base64.b64encode(os.urandom(32)).decode(), 'uploaded':[]}

    def persist():
        # Installation secrets belong to this synthetic local test device only.
        with os.fdopen(os.open(state_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), 'w') as output:
            json.dump(state, output, ensure_ascii=False, indent=2)

    persist()
    registration = api('POST', '/device/register', {key:state[key] for key in ('installation_id','registration_secret')} |
                       {'platform':'iOS', 'os_version':'18.1', 'client_version':'photo-demo', 'capabilities':{'location':False,'test_fixture':True}})
    state.update(registration)
    persist()
    device_id, token = state['device_id'], state['device_token']
    api('PATCH', '/devices/' + device_id, {'name':('手机照片测试' if PORTRAIT else '照片测试') + ' · ' + RATIO + '（10 张）', 'note':f'10 张 {WIDTH} × {HEIGHT} Unsplash 测试照片，仅用于图片页面验收，并非真实设备采集。'})
    for photo in photos:
        if photo['index'] in state['uploaded']:
            continue
        device = api('GET', '/devices/' + device_id)
        command = api('POST', '/devices/' + device_id + '/commands', {'type':'GET_IMAGE'})
        command_id = command['command']['id'] if 'command' in command else command['id']
        metadata = {'report_id':'photo-demo-' + str(photo['index']), 'device_id':device_id, 'command_id':command_id,
                    'assignment_id':device['assignment_id'] or '', 'assignment_version':device['assignment_version'],
                    'capture_source':'uikit_window', 'capture_scope':'active_window',
                    'captured_at':datetime.now(timezone.utc).isoformat(), 'pixel_width':WIDTH, 'pixel_height':HEIGHT, 'is_cached':False}
        boundary = 'JKPhotoDemo' + uuid.uuid4().hex
        payload = (f'--{boundary}\r\nContent-Disposition: form-data; name="metadata"\r\n\r\n'.encode() + json.dumps(metadata).encode() +
                   f'\r\n--{boundary}\r\nContent-Disposition: form-data; name="image"; filename="{photo["file"]}"\r\nContent-Type: image/png\r\n\r\n'.encode() +
                   (OUTPUT / photo['file']).read_bytes() + f'\r\n--{boundary}--\r\n'.encode())
        api('POST', '/device/images', payload, token, 'multipart/form-data; boundary=' + boundary)
        # Report a successful synthetic command so the demo does not leave pending work behind.
        api('POST', '/device/commands/' + command_id + '/result', {'status':'succeeded'}, token)
        state['uploaded'].append(photo['index'])
        persist()
        print(f'Uploaded test photo {photo["index"]}/10')
    history = api('GET', '/devices/' + device_id + '/images')['items']
    assert len(history) == 10 and all(image['width'] == WIDTH and image['height'] == HEIGHT for image in history)
    assert len({photo['sha256'] for photo in photos}) == 10
    public = {'device_id':device_id, 'url':url + '/#/devices/' + device_id, 'images':len(history), 'dimensions':f'{WIDTH} × {HEIGHT}', 'test_data':True}
    (OUTPUT / 'result.json').write_text(json.dumps(public, ensure_ascii=False, indent=2) + '\n')
    api('POST', '/auth/logout')
    print(json.dumps(public, ensure_ascii=False))


if __name__ == '__main__':
    main()
