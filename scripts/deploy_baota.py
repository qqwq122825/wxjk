#!/usr/bin/env python3
"""Build a Git checkout for Baota; configure its executable and env file in the GUI."""
from pathlib import Path
import hashlib,json,os,platform,pwd,shutil,subprocess,tarfile,tempfile,urllib.request
ROOT=Path(__file__).resolve().parents[1]
SERVER=ROOT/'server'
if platform.system()!='Linux' or platform.machine() not in ('x86_64','amd64'):
    raise SystemExit('This deployment recipe targets Linux amd64')
if not shutil.which('gcc'):raise SystemExit('GCC is required for SQLite CGO')
if not (SERVER/'public/index.html').is_file() or not list((SERVER/'public/assets').glob('*.js')):
    raise SystemExit('Commit built server/public assets before deployment')
go=ROOT/'.toolchains/go/bin/go'
if not go.exists():
    releases=json.load(urllib.request.urlopen('https://go.dev/dl/?mode=json&include=all',timeout=45))
    release=next(item for item in releases if item['version']=='go1.27.4')
    archive=next(item for item in release['files'] if item['os']=='linux' and item['arch']=='amd64' and item['kind']=='archive')
    directory=ROOT/'.toolchains';directory.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(dir=directory) as temporary:
        package=Path(temporary)/archive['filename']
        with urllib.request.urlopen('https://go.dev/dl/'+archive['filename'],timeout=120) as response,package.open('wb') as output:shutil.copyfileobj(response,output)
        if hashlib.sha256(package.read_bytes()).hexdigest()!=archive['sha256']:raise SystemExit('Go SDK checksum mismatch')
        with tarfile.open(package) as content:
            for member in content.getmembers():
                if not (Path(temporary)/member.name).resolve().is_relative_to(Path(temporary).resolve()):raise SystemExit('Invalid SDK archive path')
            content.extractall(temporary)
        shutil.move(str(Path(temporary)/'go'),str(directory/'go'))
    print('Official Go SDK checksum verified',flush=True)
env=os.environ.copy();env.update(CGO_ENABLED='1',GOTOOLCHAIN='local',GOCACHE=str(ROOT/'.cache/go-build'),GOPATH=str(ROOT/'.cache/gopath'))
output=ROOT/'build/server';output.mkdir(parents=True,exist_ok=True)
subprocess.run([str(go),'build','-mod=readonly','-trimpath','-ldflags=-s -w','-o',str(output/'jk-monitor.new'),'.'],cwd=SERVER,env=env,check=True)
os.replace(output/'jk-monitor.new',output/'jk-monitor')
data=SERVER/'.data';data.mkdir(exist_ok=True)
config=SERVER/'.env'
if not config.exists():
    config.write_text('LISTEN_ADDR=127.0.0.1:18080\nDATA_DIR='+str(data)+'\nCOOKIE_SECURE=true\nSETUP_TOKEN='+os.urandom(32).hex()+'\n')
www=pwd.getpwnam('www')
for directory in [output,data]:
    os.chown(directory,www.pw_uid,www.pw_gid)
    for current,dirs,files in os.walk(directory):
        for name in dirs+files:os.chown(Path(current)/name,www.pw_uid,www.pw_gid)
os.chown(config,www.pw_uid,www.pw_gid);config.chmod(0o600);data.chmod(0o700)
print('WXJK_BUILD_OK',flush=True)
print('EXECUTABLE='+str(output/'jk-monitor'),flush=True)
print('ENV_FILE='+str(config),flush=True)
print('LISTEN_ADDR=127.0.0.1:18080',flush=True)
print('Configure restart, boot start, domain and HTTPS in Baota Go project settings.',flush=True)
