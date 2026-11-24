import http.client,json,subprocess,hashlib

def sql(q):
 p=subprocess.run(['mysql','-h10.99.0.13','-uisucon','-pisucon','-N','-B','isupipe','-e',q],capture_output=True,text=True,check=True)
 return [line.split('\t') for line in p.stdout.strip().splitlines()]
cookie=''
def api(path,body=None):
 global cookie
 c=http.client.HTTPConnection('127.0.0.1',8080,timeout=30)
 headers={'Host':'pipe.u.isucon.dev','Cookie':cookie}
 c.request('GET' if body is None else 'POST',path,body=None if body is None else json.dumps(body),headers=headers)
 r=c.getresponse();data=r.read();status=r.status
 if r.getheader('Set-Cookie'):cookie=r.getheader('Set-Cookie').split(';')[0]
 c.close()
 assert status==200,(path,status,data[:300])
 return json.loads(data) if data else None
api('/api/login',{'username':'test001','password':'test'})
expected=int(sql('SELECT COALESCE(SUM(tip),0) FROM livecomments')[0][0]);actual=api('/api/payment')['total_tip'];assert expected==actual,(expected,actual)
users=sql('''WITH scores AS (SELECT u.id,u.name,COALESCE(r.n,0)+COALESCE(c.n,0) score FROM users u
LEFT JOIN (SELECT l.user_id,COUNT(*) n FROM reactions r JOIN livestreams l ON l.id=r.livestream_id GROUP BY l.user_id) r ON r.user_id=u.id
LEFT JOIN (SELECT l.user_id,SUM(c.tip) n FROM livecomments c JOIN livestreams l ON l.id=c.livestream_id GROUP BY l.user_id) c ON c.user_id=u.id)
SELECT id,name,ROW_NUMBER() OVER(ORDER BY score DESC,name DESC) ranking FROM scores ORDER BY score DESC,name DESC LIMIT 15''')
for uid,name,rank in users:
 stats=api('/api/user/'+name+'/statistics')
 assert stats['rank']==int(rank),(name,stats['rank'],rank)
 tip,count=map(int,sql('SELECT COALESCE(SUM(c.tip),0),COUNT(*) FROM livecomments c JOIN livestreams l ON l.id=c.livestream_id WHERE l.user_id='+uid)[0])
 assert (stats['total_tip'],stats['total_livecomments'])==(tip,count),(name,stats,tip,count)
 reactions=int(sql('SELECT COUNT(*) FROM reactions r JOIN livestreams l ON l.id=r.livestream_id WHERE l.user_id='+uid)[0][0]);assert stats['total_reactions']==reactions
 viewers=int(sql('SELECT COUNT(*) FROM livestream_viewers_history v JOIN livestreams l ON l.id=v.livestream_id WHERE l.user_id='+uid)[0][0]);assert stats['viewers_count']==viewers
 emoji=sql('SELECT r.emoji_name FROM reactions r JOIN livestreams l ON l.id=r.livestream_id WHERE l.user_id='+uid+' GROUP BY r.emoji_name ORDER BY COUNT(*) DESC,r.emoji_name DESC LIMIT 1')
 assert stats['favorite_emoji']==(emoji[0][0] if emoji else '')
streams=sql('''SELECT l.id,ROW_NUMBER() OVER(ORDER BY COALESCE(r.n,0)+COALESCE(c.n,0) DESC,l.id DESC) ranking FROM livestreams l
LEFT JOIN (SELECT livestream_id,COUNT(*) n FROM reactions GROUP BY livestream_id) r ON r.livestream_id=l.id
LEFT JOIN (SELECT livestream_id,SUM(tip) n FROM livecomments GROUP BY livestream_id) c ON c.livestream_id=l.id
ORDER BY COALESCE(r.n,0)+COALESCE(c.n,0) DESC,l.id DESC LIMIT 15''')
for lid,rank in streams:
 stats=api('/api/livestream/'+lid+'/statistics');assert stats['rank']==int(rank),(lid,stats['rank'],rank)
 max_tip=int(sql('SELECT COALESCE(MAX(tip),0) FROM livecomments WHERE livestream_id='+lid)[0][0]);assert stats['max_tip']==max_tip
 for tab,col in [('reactions','total_reactions'),('livecomment_reports','total_reports'),('livestream_viewers_history','viewers_count')]:
  n=int(sql('SELECT COUNT(*) FROM '+tab+' WHERE livestream_id='+lid)[0][0]);assert stats[col]==n,(lid,col,stats[col],n)
print(json.dumps({'verified_users':len(users),'verified_streams':len(streams),'total_tip':expected}))
