#!/usr/bin/env python3
import base64, hashlib, http.client, json, ssl, sys, time
from pathlib import Path
state_path=Path(__file__).with_name('reboot_expected.json')
mode=sys.argv[1]
cookie=''
def req(method,path,body=None):
    global cookie
    conn=http.client.HTTPSConnection('10.99.0.11',context=ssl._create_unverified_context(),timeout=20)
    headers={'Host':'pipe.u.isucon.dev','Cookie':cookie}
    if body is not None:
        body=json.dumps(body).encode();headers['Content-Type']='application/json'
    conn.request(method,path,body=body,headers=headers)
    r=conn.getresponse();data=r.read()
    sc=r.getheader('Set-Cookie')
    if sc:cookie=sc.split(';')[0]
    conn.close()
    if r.status>=400:raise RuntimeError((method,path,r.status,data[:500]))
    if r.getheader('Content-Type','').startswith('application/json'):return json.loads(data)
    return data
if mode=='before':
    name='durability'+str(int(time.time()))
    user=req('POST','/api/register',{'name':name,'display_name':'再起動検証','description':'永続化の確認','password':'durable-test-password','theme':{'dark_mode':True}})
    req('POST','/api/login',{'username':name,'password':'durable-test-password'})
    image=b'\xff\xd8\xff\xe0durability-test-image\xff\xd9'
    req('POST','/api/icon',{'image':base64.b64encode(image).decode()})
    stream=req('POST','/api/livestream/reservation',{'tags':[1,2],'title':'再起動テスト','description':'保存確認','playlist_url':'https://media.xiii.isucon.dev/test.m3u8','thumbnail_url':'https://media.xiii.isucon.dev/test.jpg','start_at':1727744400,'end_at':1727748000})
    sid=stream['id']
    req('POST',f'/api/livestream/{sid}/enter')
    comment=req('POST',f'/api/livestream/{sid}/livecomment',{'comment':'保存するコメント','tip':123})
    req('POST',f'/api/livestream/{sid}/reaction',{'emoji_name':'innocent'})
    req('POST',f'/api/livestream/{sid}/livecomment/{comment["id"]}/report')
    req('POST',f'/api/livestream/{sid}/moderate',{'ng_word':'永続化NGワード'})
    paths=['/api/payment','/api/user/'+name,'/api/user/'+name+'/theme','/api/user/'+name+'/livestream','/api/user/'+name+'/statistics']
    paths += [f'/api/livestream/{sid}'+s for s in ['', '/livecomment','/reaction','/statistics','/report','/ngwords']]
    values={p:req('GET',p) for p in paths}
    values['icon_sha256']=hashlib.sha256(req('GET',f'/api/user/{name}/icon')).hexdigest()
    state_path.write_text(json.dumps({'name':name,'cookie':cookie,'values':values},ensure_ascii=False,indent=2))
    print('Saved expected results for',len(paths),'JSON endpoints and icon;',name)
elif mode=='after':
    state=json.loads(state_path.read_text());cookie=state['cookie'];name=state['name']
    for p,want in state['values'].items():
        if p=='icon_sha256':got=hashlib.sha256(req('GET',f'/api/user/{name}/icon')).hexdigest()
        else:got=req('GET',p)
        assert got==want,(p,got,want)
    req('POST','/api/login',{'username':name,'password':'durable-test-password'})
    assert req('GET','/api/user/me')['name']==name
    print('PASS: all persisted data, old session and bcrypt login survived restart')
else:raise RuntimeError('mode must be before/after')
