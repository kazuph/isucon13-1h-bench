#!/usr/bin/env python3
"""Verify conditional icon cache correctness and the two-second update bound."""
import hashlib,json,pathlib,ssl,time,urllib.error,urllib.request
root=pathlib.Path(__file__).resolve().parents[1]
data=json.loads((root/'logs/persistence.json').read_text())
ctx=ssl._create_unverified_context()
path='/api/user/'+data['name']+'/icon'
def request(method,path,body=None,etag=None):
    h={'Host':'pipe.u.isucon.dev','Cookie':data['cookie'],'Content-Type':'application/json'}
    if etag:h['If-None-Match']='"'+etag+'"'
    r=urllib.request.Request('https://10.99.0.11'+path,data=body,headers=h,method=method)
    try:
        with urllib.request.urlopen(r,context=ctx,timeout=5) as f:return f.status,f.read(),f.headers
    except urllib.error.HTTPError as f:return f.code,f.read(),f.headers
code,body,_=request('GET',path);assert code==200
old=hashlib.sha256(body).hexdigest()
for _ in range(2):
    code,_,_=request('GET',path,etag=old);assert code==304
import base64
newbody=body+b'\n'
new=hashlib.sha256(newbody).hexdigest()
code,_,_=request('POST','/api/icon',json.dumps({'image':base64.b64encode(newbody).decode()}).encode());assert code==201
start=time.monotonic()
code,_,_=request('GET',path,etag=new);assert code==304
while True:
    code,b,h=request('GET',path,etag=old)
    if code==200:
        assert hashlib.sha256(b).hexdigest()==new
        break
    assert code==304
    assert time.monotonic()-start<2,'Icon cache stale for two seconds'
    time.sleep(.05)
elapsed=time.monotonic()-start
assert elapsed<2,(elapsed,'freshness bound exceeded')
# Restore the prepared state so the separate reboot test can compare it.
code,_,_=request('POST','/api/icon',json.dumps({'image':base64.b64encode(body).decode()}).encode());assert code==201
print('PASS: new icon hash validated immediately; old ETag expired in %.3fs'%elapsed)
