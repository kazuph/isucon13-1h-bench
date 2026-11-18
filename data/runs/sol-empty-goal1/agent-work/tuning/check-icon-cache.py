import base64,hashlib,json,time,urllib.request,urllib.error
bases=['http://10.99.0.11:8080','http://10.99.0.13:8080']
def request(base,path,data=None,headers=None):
 h={'Host':'pipe.u.isucon.dev'}
 if headers: h.update(headers)
 if data is not None:
  data=json.dumps(data).encode();h['Content-Type']='application/json'
 req=urllib.request.Request(base+path,data=data,headers=h)
 try:
  with urllib.request.urlopen(req,timeout=5) as r: return r.status,r.headers,r.read()
 except urllib.error.HTTPError as r: return r.code,r.headers,r.read()
status,h,_=request(bases[0],'/api/login',{'username':'test001','password':'test'})
assert status==200,status
cookie=h['Set-Cookie'].split(';',1)[0]
auth={'Cookie':cookie}
image=request(bases[0],'/api/user/test001/icon')[2]+b'\n'
new_hash=hashlib.sha256(image).hexdigest()
for base in bases:
 assert request(base,'/api/user/test001',headers=auth)[0]==200
 assert request(base,'/api/user/test001/livestream',headers=auth)[0]==200
start=time.monotonic()
status,_,body=request(bases[0],'/api/icon',{'image':base64.b64encode(image).decode()},auth)
assert status==201,(status,body)
for base in bases:
 while True:
  status,_,body=request(base,'/api/user/test001',headers=auth)
  user=json.loads(body)
  status,_,body=request(base,'/api/user/test001/livestream',headers=auth)
  streams=json.loads(body)
  if user['icon_hash']==new_hash and all(s['owner']['icon_hash']==new_hash for s in streams): break
  assert time.monotonic()-start<2,(base,'cache exceeded 2 seconds')
  time.sleep(.03)
 status,h,body=request(base,'/api/user/test001/icon')
 assert status==200 and hashlib.sha256(body).hexdigest()==new_hash
 status,_,_=request(base,'/api/user/test001/icon',headers={'If-None-Match':'"'+new_hash+'"'})
 assert status==304,status
 print('PASS',base,'icon update and conditional GET',round(time.monotonic()-start,3),'seconds')
