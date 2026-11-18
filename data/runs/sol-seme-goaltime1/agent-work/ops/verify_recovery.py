#!/usr/bin/env python3
"""Snapshot public/API reads and every persistent table around a server reboot."""
import argparse, hashlib, http.client, json, pathlib, subprocess

parser=argparse.ArgumentParser();parser.add_argument('output');args=parser.parse_args()
def sql(query):
 return subprocess.check_output(['ssh','s3','sudo','mysql','-N','isupipe','-e',"'"+query+"'"],text=True).strip()
conn=http.client.HTTPConnection('10.99.0.12',8080,timeout=20)
def request(path,method='GET',body=None,cookie=None):
 headers={'Host':'pipe.u.isucon.dev','Content-Type':'application/json'}
 if cookie:headers['Cookie']=cookie
 conn.request(method,path,body,headers);r=conn.getresponse();data=r.read()
 if r.status!=200:raise RuntimeError((path,r.status,data[:300]))
 return data,r.getheader('Set-Cookie')
_,cookie=request('/api/login','POST',json.dumps({'username':'test001','password':'test'}))
cookie=cookie.split(';',1)[0]
latest=sql('SELECT MAX(id) FROM livestreams')
owner=sql('SELECT u.name FROM users u JOIN livestreams l ON l.user_id=u.id WHERE l.id='+latest)
iconuser=sql('SELECT u.name FROM users u JOIN icons i ON i.user_id=u.id ORDER BY i.id DESC LIMIT 1')
paths=['/api/payment','/api/tag','/api/livestream/search?limit=50','/api/user/me','/api/user/'+owner,'/api/user/'+owner+'/statistics','/api/user/'+owner+'/icon','/api/user/'+iconuser+'/icon']
paths+=['/api/livestream/'+latest+suffix for suffix in ['', '/statistics','/livecomment','/reaction','/ngwords']]
api={}
for path in paths:
 data,_=request(path,cookie=cookie)
 if path.endswith('/icon'):api[path]={'sha256':hashlib.sha256(data).hexdigest(),'bytes':len(data)}
 else:api[path]=json.loads(data)
tables=sql('SHOW TABLES').splitlines()
checksums=sql('CHECKSUM TABLE '+','.join(tables))
result={'api':api,'checksums':checksums,'paths':paths}
pathlib.Path(args.output).write_text(json.dumps(result,ensure_ascii=False,sort_keys=True,indent=2)+'\n')
print(json.dumps({'output':args.output,'api_endpoints':len(paths),'tables':len(tables),'payment':api['/api/payment']},ensure_ascii=False))
