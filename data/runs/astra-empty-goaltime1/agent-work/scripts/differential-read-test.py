#!/usr/bin/env python3
"""Compare reference and optimized responses on a frozen development snapshot."""
import urllib.request,urllib.error,json,hashlib,sys
base=sys.argv[1] if len(sys.argv)>1 else 'http://127.0.0.1:18080'
optimized=sys.argv[2] if len(sys.argv)>2 else 'http://127.0.0.1:18081'
cookie=''
def request(url,path,body=None,headers=None):
    h={'Host':'pipe.u.isucon.dev','Cookie':cookie}
    if headers:h.update(headers)
    if body is not None:h['Content-Type']='application/json'
    req=urllib.request.Request(url+path,data=None if body is None else json.dumps(body).encode(),headers=h)
    try: response=urllib.request.urlopen(req,timeout=120)
    except urllib.error.HTTPError as error:response=error
    return response.status,response.headers,response.read()
status,headers,body=request(base,'/api/login',{'username':'test001','password':'test'})
assert status==200,(status,body)
cookie=headers['Set-Cookie'].split(';',1)[0]
def normalize(value):
    if isinstance(value,dict):
        return {k:sorted((normalize(t) for t in v),key=lambda t:t['id']) if k=='tags' else normalize(v) for k,v in value.items()}
    if isinstance(value,list):return [normalize(v) for v in value]
    return value
count=0
def compare(path):
    global count
    a=request(base,path);b=request(optimized,path)
    assert a[0]==b[0],(path,a[0],b[0],a[2][:200],b[2][:200])
    assert a[0]==200,(path,a[0],a[2][:200])
    if a[1].get_content_type()=='application/json':
        av=normalize(json.loads(a[2]));bv=normalize(json.loads(b[2]))
        assert av==bv,(path,'JSON mismatch',str(av)[:200],str(bv)[:200])
    else:assert a[2]==b[2],(path,'binary mismatch')
    count+=1;print('OK',path,flush=True)
    return json.loads(a[2]) if a[1].get_content_type()=='application/json' else a[2]
for path in ['/api/tag','/api/payment','/api/user/me','/api/user/test001','/api/user/test001/theme','/api/user/test001/livestream']:
    compare(path)
streams=compare('/api/livestream/search?limit=5')
extra_streams=[compare('/api/livestream/'+str(int(stream_id))) for stream_id in sys.argv[3:]]
for stream in streams[:3]+extra_streams:
    streamid=stream['id']
    compare('/api/livestream/'+str(streamid))
    compare('/api/livestream/'+str(streamid)+'/livecomment?limit=10')
    compare('/api/livestream/'+str(streamid)+'/reaction?limit=10')
    compare('/api/user/'+stream['owner']['name'])
for user in ['test001']+[stream['owner']['name'] for stream in streams[:2]]:
    path='/api/user/'+user+'/icon'
    data=compare(path);etag='"'+hashlib.sha256(data).hexdigest()+'"'
    status,_,body=request(optimized,path,headers={'If-None-Match':etag})
    assert status==304 and not body,(path,status)
    status,_,body=request(optimized,path,headers={'If-None-Match':'"not-current"'})
    assert status==200 and body==data,(path,status)
compare('/api/user/test001/statistics')
for stream in extra_streams:
    compare('/api/user/'+stream['owner']['name']+'/statistics')
    compare('/api/livestream/'+str(stream['id'])+'/statistics')
if streams: compare('/api/livestream/'+str(streams[0]['id'])+'/statistics')
print('Compared',count,'responses and icon conditional requests successfully',flush=True)
