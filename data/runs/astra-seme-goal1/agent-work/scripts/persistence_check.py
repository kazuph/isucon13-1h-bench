#!/usr/bin/env python3
"""Create real application state and compare all relevant APIs after reboot."""
import base64, hashlib, http.cookiejar, json, pathlib, ssl, sys, time, urllib.request, urllib.error
ROOT=pathlib.Path(__file__).resolve().parents[1]
OUT=ROOT/'logs'/'persistence.json'
jar=http.cookiejar.CookieJar()
opener=urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
# Direct private-network API access is used for the persistence check.
base='http://10.99.0.12:8080'
cookie=''
def call(method,path,obj=None,headers=None):
    h={'Content-Type':'application/json','Host':'pipe.u.isucon.dev'}
    if cookie: h['Cookie']=cookie
    h.update(headers or {})
    req=urllib.request.Request(base+path,data=None if obj is None else json.dumps(obj).encode(),headers=h,method=method)
    try:
        with opener.open(req,timeout=20) as r:return r.status,r.read(),r.headers
    except urllib.error.HTTPError as e:return e.code,e.read(),e.headers

def expect(method,path,obj=None,status=200):
    code,b,h=call(method,path,obj)
    assert code==status,(method,path,code,b)
    return json.loads(b) if b else None

if sys.argv[1]=='prepare':
    name='persist'+str(time.time_ns())
    expect('POST','/api/register',{'name':name,'password':'verification-password','display_name':'Persistence test','description':'reboot verification','theme':{'dark_mode':True}},201)
    code,b,h=call('POST','/api/login',{'username':name,'password':'verification-password'})
    assert code==200,(code,b)
    cookie=h['Set-Cookie'].split(';',1)[0]
    code,img,_=call('GET','/api/user/test001/icon');assert code==200
    expect('POST','/api/icon',{'image':base64.b64encode(img).decode()},201)
    for n in range(100):
        start=1700874000+(7000+n)*3600
        code,b,_=call('POST','/api/livestream/reservation',{'tags':[1,2],'title':'Persistence stream','description':'durability verification','playlist_url':'https://media.xiii.isucon.dev/persist.m3u8','thumbnail_url':'https://media.xiii.isucon.dev/persist.jpg','start_at':start,'end_at':start+3600})
        if code==201:stream=json.loads(b);break
        assert code==400,(code,b)
    else:raise AssertionError('No available reservation slot')
    sid=stream['id'];prefix=f'/api/livestream/{sid}'
    expect('POST',prefix+'/enter',{},200)
    expect('POST',prefix+'/reaction',{'emoji_name':'innocent'},201)
    expect('POST',prefix+'/livecomment',{'comment':'persistent tip','tip':4321},201)
    comment=expect('POST',prefix+'/livecomment',{'comment':'remove-this-message','tip':13},201)
    expect('POST',prefix+f"/livecomment/{comment['id']}/report",{},201)
    expect('POST',prefix+'/moderate',{'ng_word':'remove-this-message'},201)
    paths=['/api/user/me',f'/api/user/{name}',f'/api/user/{name}/theme',f'/api/user/{name}/livestream',f'/api/user/{name}/statistics','/api/livestream',prefix,prefix+'/livecomment',prefix+'/reaction',prefix+'/report',prefix+'/ngwords',prefix+'/statistics','/api/payment']
    results={p:expect('GET',p) for p in paths}
    assert results[prefix+'/statistics']['max_tip']==4321
    assert len(results[prefix+'/livecomment'])==1
    assert results[prefix+'/report'][0]['livecomment']['id']==comment['id']
    data={'name':name,'cookie':cookie,'results':results,'icon_hash':hashlib.sha256(img).hexdigest()}
    OUT.write_text(json.dumps(data,ensure_ascii=False,indent=2))
    print('Prepared persistent user, icon, reservation, tip, reaction, viewers, report, moderation and aggregate checks')
elif sys.argv[1]=='check':
    data=json.loads(OUT.read_text());cookie=data['cookie']
    for p,want in data['results'].items():
        got=expect('GET',p)
        assert got==want,(p,want,got)
    path=f"/api/user/{data['name']}/icon"
    code,b,_=call('GET',path);assert code==200
    assert hashlib.sha256(b).hexdigest()==data['icon_hash']
    code,b,_=call('GET',path,headers={'If-None-Match':'"'+data['icon_hash']+'"'});assert code==304
    print('PASS: all saved API data and icon conditionals survived reboot')
else:raise SystemExit('prepare | check')
