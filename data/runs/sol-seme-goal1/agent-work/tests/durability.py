#!/usr/bin/env python3
"""Exercise each persistent mutation, then compare responses after restarting all hosts.
Run with create/check; state is saved locally and never resets the benchmark.
"""
import base64, hashlib, http.client, json, pathlib, ssl, sys, time
store=pathlib.Path(__file__).with_name('durability-state.json')
ctx=ssl._create_unverified_context()
cookie=''
def request(method,path,payload=None):
    con=http.client.HTTPSConnection('10.99.0.11',context=ctx,timeout=20)
    headers={'Host':'pipe.u.isucon.dev','Content-Type':'application/json'}
    if cookie: headers['Cookie']=cookie
    con.request(method,path,json.dumps(payload).encode() if payload is not None else None,headers)
    r=con.getresponse();data=r.read();headers=dict(r.getheaders());con.close()
    if r.status>=400: raise RuntimeError((method,path,r.status,data[:500]))
    return (json.loads(data) if data and headers.get('Content-Type','').startswith('application/json') else data),headers
if sys.argv[1]=='create':
    name='durable'+str(time.time_ns())
    user,_=request('POST','/api/register',{'name':name,'display_name':'永続化検証','description':'restart check','password':'persistence-test','theme':{'dark_mode':True}})
    _,h=request('POST','/api/login',{'username':name,'password':'persistence-test'});cookie=h['Set-Cookie'].split(';')[0]
    image=b'\xff\xd8\xff\xe0durability-icon'
    request('POST','/api/icon',{'image':base64.b64encode(image).decode()})
    stream,_=request('POST','/api/livestream/reservation',{'title':'durability','description':'test','playlist_url':'https://example.com/a.m3u8','thumbnail_url':'https://example.com/a.jpg','start_at':1720000000,'end_at':1720003600,'tags':[1,2]})
    sid=stream['id'];prefix=f'/api/livestream/{sid}'
    request('POST',prefix+'/enter')
    request('POST',prefix+'/reaction',{'emoji_name':'tada'})
    comment,_=request('POST',prefix+'/livecomment',{'comment':'keep this','tip':123})
    request('POST',prefix+f"/livecomment/{comment['id']}/report")
    request('POST',prefix+'/livecomment',{'comment':'remove-me','tip':7})
    request('POST',prefix+'/moderate',{'ng_word':'remove-me'})
    paths=[f'/api/user/{name}',f'/api/user/{name}/theme',f'/api/user/{name}/livestream',f'/api/user/{name}/statistics',f'/api/user/{name}/icon',prefix,prefix+'/livecomment',prefix+'/reaction',prefix+'/report',prefix+'/ngwords',prefix+'/statistics','/api/payment']
    expected={p:base64.b64encode(json.dumps(request('GET',p)[0],sort_keys=True).encode()).decode() if not p.endswith('/icon') else hashlib.sha256(request('GET',p)[0]).hexdigest() for p in paths}
    store.write_text(json.dumps({'cookie':cookie,'expected':expected,'name':name}))
    print('Created durable user, icon, reservation, viewer, reaction, comments, report and moderation')
else:
    state=json.loads(store.read_text());cookie=state['cookie']
    for p,want in state['expected'].items():
        value=request('GET',p)[0]
        got=hashlib.sha256(value).hexdigest() if p.endswith('/icon') else base64.b64encode(json.dumps(value,sort_keys=True).encode()).decode()
        assert got==want,(p,'changed after restart')
    print('All 12 persisted endpoint responses match exactly')
