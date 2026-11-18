import json, urllib.request
servers=['http://10.99.0.11:8080','http://10.99.0.13:8080']
req=urllib.request.Request(servers[0]+'/api/login',data=json.dumps({'username':'test001','password':'test'}).encode(),headers={'Content-Type':'application/json','Host':'pipe.u.isucon.dev'})
with urllib.request.urlopen(req) as r:
    cookie=r.headers['Set-Cookie'].split(';',1)[0]
paths=['/api/user/me','/api/livestream/search?limit=50','/api/user/test001/livestream','/api/user/test001/statistics']
req=urllib.request.Request(servers[0]+'/api/livestream/search?limit=10',headers={'Cookie':cookie})
streams=json.load(urllib.request.urlopen(req))
for stream in streams[:3]:
    sid=stream['id']
    paths += [f'/api/livestream/{sid}/livecomment?limit=50',f'/api/livestream/{sid}/reaction?limit=50',f'/api/livestream/{sid}/statistics']
for path in paths:
    responses=[]
    for base in servers:
        req=urllib.request.Request(base+path,headers={'Cookie':cookie,'Host':'pipe.u.isucon.dev'})
        responses.append(json.load(urllib.request.urlopen(req)))
    assert responses[0]==responses[1],path
    print('MATCH',path)
