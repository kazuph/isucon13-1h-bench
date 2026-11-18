from server_address import API_ADDRESS
import http.client,json,base64,hashlib,os,time,sys
PATH='/home/isucon/work/logs/reboot-audit.json'
def req(method,path,data=None,cookie=None):
 c=http.client.HTTPConnection(API_ADDRESS,8080,timeout=10);headers={}
 if data is not None: data=json.dumps(data);headers['Content-Type']='application/json'
 if cookie:headers['Cookie']=cookie
 c.request(method,path,data,headers);r=c.getresponse();body=r.read();result=(r.status,dict(r.getheaders()),body);c.close();return result
def jreq(method,path,data=None,cookie=None):
 code,headers,body=req(method,path,data,cookie)
 if code>=400:raise RuntimeError((code,path,body))
 return (json.loads(body) if body else None),headers
if sys.argv[1]=='create':
 name='durabilityaudit'+str(int(time.time()))
 user,_=jreq('POST','/api/register',{'name':name,'display_name':'Persistence audit','description':'reboot verification','password':'audit-password','theme':{'dark_mode':True}})
 _,headers=jreq('POST','/api/login',{'username':name,'password':'audit-password'});cookie=headers['Set-Cookie'].split(';')[0]
 # Existing valid JPEG is changed only by adding a JPEG comment segment.
 code,_,jpeg=req('GET','/api/user/'+name+'/icon');assert code==200
 image=jpeg[:2]+b'\xff\xfe\x00\x07audit'+jpeg[2:]
 jreq('POST','/api/icon',{'image':base64.b64encode(image).decode()},cookie)
 stream,_=jreq('POST','/api/livestream/reservation',{'tags':[1,2],'title':'Reboot audit','description':'Durable stream','playlist_url':'https://example.invalid/a.m3u8','thumbnail_url':'https://example.invalid/a.jpg','start_at':1732492800,'end_at':1732496400},cookie)
 comment,_=jreq('POST','/api/livestream/'+str(stream['id'])+'/livecomment',{'comment':'Reboot audit comment','tip':123},cookie)
 reaction,_=jreq('POST','/api/livestream/'+str(stream['id'])+'/reaction',{'emoji_name':'smile'},cookie)
 user,_=jreq('GET','/api/user/me',cookie=cookie)
 state={'name':name,'cookie':cookie,'user':user,'stream':stream,'comment':comment,'reaction':reaction,'icon_hash':hashlib.sha256(image).hexdigest()}
 json.dump(state,open(PATH,'w'));print('created durable user, icon, livestream, comment, reaction',name,stream['id'])
else:
 state=json.load(open(PATH));cookie=state['cookie'];sid=str(state['stream']['id']);user,_=jreq('GET','/api/user/me',cookie=cookie);assert user==state['user'],('user mismatch',user)
 code,_,image=req('GET','/api/user/'+state['name']+'/icon');assert code==200 and hashlib.sha256(image).hexdigest()==state['icon_hash'],'icon mismatch'
 stream,_=jreq('GET','/api/livestream/'+sid,cookie=cookie);assert stream==state['stream'],'stream mismatch'
 comments,_=jreq('GET','/api/livestream/'+sid+'/livecomment',cookie=cookie);assert state['comment'] in comments,'comment mismatch'
 reactions,_=jreq('GET','/api/livestream/'+sid+'/reaction',cookie=cookie);assert state['reaction'] in reactions,'reaction mismatch'
 print('PASS: user, session, icon bytes, stream, comment and reaction survived without initialize')
