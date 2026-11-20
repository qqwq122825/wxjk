#!/usr/bin/env python3
from pathlib import Path
import argparse,json,platform,plistlib,shutil,subprocess,sys,socket,tempfile,time
ROOT=Path(__file__).resolve().parents[1];P=ROOT/'build/tests/startup';P.mkdir(parents=True,exist_ok=True)
sys.path.insert(0,str(ROOT/'scripts'));import server as tool
sys.path.insert(0,str(ROOT/'scripts'));from test_server_ios import Browser,wait
parser=argparse.ArgumentParser();parser.add_argument('--source',type=Path,default=ROOT/'ios/jk/jk');parser.add_argument('--label',default='current');parser.add_argument('--require-debug',action='store_true');args=parser.parse_args();args.source=args.source.resolve()
out=P/'ios-builtin-tests'/args.label;out.mkdir(parents=True,exist_ok=True)
def run(*cmd,**kw):return subprocess.run(list(map(str,cmd)),check=True,**kw)
sdk=subprocess.check_output(['xcrun','--sdk','iphonesimulator','--show-sdk-path'],text=True).strip();arch='arm64' if platform.machine()=='arm64' else 'x86_64'
flags=['-fobjc-arc','-fblocks','-Wall','-Wextra','-Werror','-Wno-unused-parameter','-target',arch+'-apple-ios15.6-simulator','-isysroot',sdk]
app=out/'BuiltinStartup.app';fw=app/'Frameworks';fw.mkdir(parents=True,exist_ok=True);lib=fw/'jk.dylib'
run('xcrun','clang++','-std=c++17',*flags,'-dynamiclib','-install_name','@rpath/jk.dylib',*[args.source/n for n in ['jk.mm','JKWindowCapture.mm','JKDeviceClient.mm']],*[x for name in ['Foundation','UIKit','ImageIO','CoreGraphics','Network','Security'] for x in ['-framework',name]],'-o',lib)
identifier='com.jk.builtinstartup';ent=out/'test-entitlements.plist';ent.write_bytes(plistlib.dumps({'application-identifier':'JKTEST0000.'+identifier,'keychain-access-groups':['JKTEST0000.'+identifier]}))
run('xcrun','clang',*flags,'-I',args.source,ROOT/'ios/Tests/StartupHost.m','-framework','UIKit','-framework','Foundation',lib,'-Wl,-rpath,@executable_path/Frameworks','-Wl,-sectcreate,__TEXT,__entitlements,'+str(ent),'-o',app/'BuiltinStartup')
certificate,key=out/'localhost.crt',out/'localhost.key';config=out/'localhost.cnf'
config.write_text('[req]\ndistinguished_name=dn\nx509_extensions=extensions\nprompt=no\n[dn]\nCN=localhost\n[extensions]\nbasicConstraints=critical,CA:TRUE\nkeyUsage=critical,keyCertSign,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:localhost,IP:127.0.0.1\n')
run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-sha256','-days','1','-config',config,'-keyout',key,'-out',certificate,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
with socket.socket() as sock:sock.bind(('127.0.0.1',0));port=sock.getsockname()[1]
URL=f'https://localhost:{port}'
info={'CFBundleExecutable':'BuiltinStartup','CFBundleIdentifier':identifier,'CFBundleName':'BuiltinStartup','CFBundlePackageType':'APPL','CFBundleVersion':'1','CFBundleShortVersionString':'1.0','MinimumOSVersion':'15.6','LSRequiresIPhoneOS':True,'UIDeviceFamily':[1,2],'UILaunchScreen':{},'JKMonitor':{'server_url':URL,'prompt_for_server':False,'startup_capture':True},'UIApplicationSceneManifest':{'UIApplicationSupportsMultipleScenes':False,'UISceneConfigurations':{'UIWindowSceneSessionRoleApplication':[{'UISceneConfigurationName':'Default','UISceneDelegateClassName':'StartupSceneDelegate'}]}}}
(app/'Info.plist').write_bytes(plistlib.dumps(info));run('codesign','--force','--sign','-',lib);run('codesign','--force','--sign','-',app)
run(tool.go_binary(),'build','-o',ROOT/'build/server/jk-monitor','.',cwd=ROOT/'server',env=tool.environment())
data=tempfile.TemporaryDirectory(prefix='startup-server-',dir=out);env=tool.environment();env.update({'LISTEN_ADDR':f'127.0.0.1:{port}','DATA_DIR':data.name,'TLS_CERT':str(certificate),'TLS_KEY':str(key),'SETUP_TOKEN':'startup-test','ENROLLMENT_TOKEN':''})
serverlog=(out/'server.log').open('w');backend=subprocess.Popen([str(ROOT/'build/server/jk-monitor')],env=env,stdout=serverlog,stderr=serverlog);device=None
try:
 browser=Browser(URL,certificate)
 def health():
  try:return browser.api('GET','/api/health')
  except OSError:return None
 wait('server',health,10)
 auth=browser.api('POST','/api/v1/auth/setup',{'username':'startupowner','password':'StartupOwner123!','setup_token':'startup-test'});browser.csrf=auth['csrf_token']
 runtime=next(r['identifier'] for r in json.loads(subprocess.check_output(['xcrun','simctl','list','runtimes','--json'],text=True))['runtimes'] if r['isAvailable'] and 'iOS-18-1' in r['identifier'])
 device=subprocess.check_output(['xcrun','simctl','create','JK Startup '+args.label,'com.apple.CoreSimulator.SimDeviceType.iPhone-16',runtime],text=True).strip()
 run('xcrun','simctl','boot',device);run('xcrun','simctl','bootstatus',device,'-b');run('xcrun','simctl','keychain',device,'add-root-cert',certificate);run('xcrun','simctl','install',device,app)
 r=subprocess.run(['xcrun','simctl','launch','--terminate-running-process','--console',device,identifier],capture_output=True,text=True,timeout=45)
 (out/'native.stdout').write_text(r.stdout);(out/'native.stderr').write_text(r.stderr)
 container=Path(subprocess.check_output(['xcrun','simctl','get_app_container',device,identifier,'data'],text=True).strip())
 report=json.loads((container/'Documents/builtin-startup.json').read_text())
 devices=browser.api('GET','/api/v1/devices')['items'];report['server_devices']=len(devices)
 images=browser.api('GET',f"/api/v1/devices/{devices[0]['id']}/images")['items'] if devices else []
 report['server_images']=len(images)
 if len(images)!=2:report['passed']=False;report['failures'].append('actual server must receive exactly two automatic JPGs')
 if args.require_debug:
  diagnostic=container/'Library/Caches/JKMonitor/jk-monitor.log'
  text=diagnostic.read_text() if diagnostic.exists() else ''
  console=r.stdout+r.stderr
  required=['[JKMonitor][load]','[JKMonitor][bootstrap]','[JKMonitor][client_state]','[JKMonitor][register_http]','[JKMonitor][jpeg_ready]','[JKMonitor][upload_http]']
  report['debug_console_markers']=all(marker in console for marker in required)
  report['debug_file_markers']=all(marker in text for marker in required)
  report['debug_no_secret_fields']=not any(marker in text for marker in ['Bearer ','device_token','registration_secret','PNGData','imageData'])
  report['debug_file_private']=diagnostic.exists() and diagnostic.stat().st_mode & 0o777 == 0o600
  if diagnostic.exists():shutil.copy2(diagnostic,out/'jk-monitor.log')
  for key in ['debug_console_markers','debug_file_markers','debug_no_secret_fields','debug_file_private']:
   if not report[key]:report['passed']=False;report['failures'].append(key)
 (out/'report.json').write_text(json.dumps(report,ensure_ascii=False,indent=2))
 if (container/'Documents/startup-window.jpg').exists():shutil.copy2(container/'Documents/startup-window.jpg',out/'startup-window.jpg')
 print(json.dumps(report,ensure_ascii=False,indent=2),flush=True)
 passed=report['passed']
finally:
 if device:subprocess.run(['xcrun','simctl','shutdown',device],capture_output=True);subprocess.run(['xcrun','simctl','delete',device],capture_output=True)
 backend.terminate();backend.wait(timeout=5);serverlog.close();data.cleanup()
raise SystemExit(0 if passed else 1)
