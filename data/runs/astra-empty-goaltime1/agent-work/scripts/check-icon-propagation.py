#!/usr/bin/env python3
import urllib.request,urllib.error,json,hashlib,base64,time
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
cookie=''
def request(host,path,body=None,etag=None):
    headers={'Host':'pipe.u.isucon.dev','Cookie':cookie}
    if body is not None:headers['Content-Type']='application/json'
    if etag is not None:headers['If-None-Match']=etag
    req=urllib.request.Request('http://'+host+':8080'+path,data=None if body is None else json.dumps(body).encode(),headers=headers)
    try:response=opener.open(req,timeout=5)
    except urllib.error.HTTPError as error:response=error
    return response.status,response.headers,response.read()
status,headers,body=request('10.99.0.11','/api/login',{'username':'test001','password':'test'})
assert status==200,(status,body)
cookie=headers['Set-Cookie'].split(';',1)[0]
path='/api/user/test001/icon'
status,_,original=request('10.99.0.11',path)
assert status==200
old_etag='"'+hashlib.sha256(original).hexdigest()+'"'
status,_,body=request('10.99.0.11','/api/user/test001')
assert status==200
updated=original+b'\ncache-propagation-test'
new_etag='"'+hashlib.sha256(updated).hexdigest()+'"'
try:
    started=time.monotonic()
    status,_,body=request('10.99.0.13','/api/icon',{'image':base64.b64encode(updated).decode()})
    assert status==201,(status,body)
    while True:
        status,_,body=request('10.99.0.11',path,etag=old_etag)
        elapsed=time.monotonic()-started
        if status==200 and body==updated:break
        assert status==304 and elapsed<2,(status,elapsed,body[:100])
        time.sleep(.025)
    assert elapsed<2,elapsed
    status,_,body=request('10.99.0.11',path,etag=new_etag)
    assert status==304 and not body
    status,_,body=request('10.99.0.11',path)
    assert status==200 and body==updated
    status,_,body=request('10.99.0.11','/api/user/test001')
    assert status==200 and json.loads(body)['icon_hash']==new_etag.strip('"')
    print(json.dumps({'cross_host_update_visible_seconds':round(elapsed,3),'conditional_304':True,'unconditional_200':True,'user_hash_matches':True}))
finally:
    status,_,body=request('10.99.0.13','/api/icon',{'image':base64.b64encode(original).decode()})
    assert status==201,(status,body)
