import hashlib,json,shlex,subprocess,sys,urllib.parse,urllib.request

def ssh(host,cmd):
 return subprocess.check_output(['ssh','-o','ConnectTimeout=3',host,cmd],text=True).strip()
base='http://10.99.0.11:8080'
req=urllib.request.Request(base+'/api/login',data=json.dumps({'username':'test001','password':'test'}).encode(),headers={'Content-Type':'application/json'})
with urllib.request.urlopen(req,timeout=5) as response:
 cookie=response.headers['Set-Cookie'].split(';',1)[0]
def get(path,binary=False):
 req=urllib.request.Request(base+path,headers={'Cookie':cookie})
 with urllib.request.urlopen(req,timeout=5) as response:
  body=response.read()
  return hashlib.sha256(body).hexdigest() if binary else json.loads(body)
if sys.argv[1]=='save':
 name=ssh('s2','sudo mysql -N -e "SELECT name FROM isupipe.users ORDER BY id DESC LIMIT 1"')
 stream_id=get('/api/livestream/search?limit=1')[0]['id']
else:
 with open(sys.argv[2]) as f: saved=json.load(f)
 name=saved['username'];stream_id=saved['stream_id']
checksums=ssh('s2','sudo mysql -N -e "CHECKSUM TABLE isupipe.users,isupipe.icons,isupipe.themes,isupipe.livestreams,isupipe.livestream_tags,isupipe.reservation_slots,isupipe.livestream_viewers_history,isupipe.livecomments,isupipe.livecomment_reports,isupipe.ng_words,isupipe.reactions,isupipe.tags"')
dns_checksums=hashlib.sha256(ssh('s1','sqlite3 /var/lib/powerdns/isudns.sqlite3 ".dump"').encode()).hexdigest()
state={'username':name,'stream_id':stream_id,'checksums':checksums,'dns_checksums':dns_checksums,
 'user':get('/api/user/'+urllib.parse.quote(name,safe='')),
 'stream':get('/api/livestream/'+str(stream_id)),
 'icon_hash':get('/api/user/'+urllib.parse.quote(name,safe='')+'/icon',True),
 'payment':get('/api/payment'),
 'dns':ssh('s1','dig +short @127.0.0.1 '+shlex.quote(name+'.u.isucon.dev'))}
if sys.argv[1]=='save':
 with open(sys.argv[2],'w') as f: json.dump(state,f,ensure_ascii=False,indent=2)
 print('Saved persisted user',name,'stream',stream_id,'and app/DNS table checksums')
else:
 assert state==saved,[(key,saved[key],state[key]) for key in state if state[key]!=saved[key]]
 print('PASS: all DB checksums, recent user/stream/icon, payment and DNS survive reboot')
