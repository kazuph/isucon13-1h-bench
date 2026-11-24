"""Verify public statistics against independent SQL while a benchmark is idle."""
import json
import shlex
import subprocess
from urllib.parse import quote


def ssh(host, command, data=None):
    return subprocess.run(["ssh", host, command], input=data, text=True,
                          capture_output=True, check=True).stdout


def query(sql):
    return ssh("s2", "sudo mysql -BN isupipe", sql+";").strip()


def api(paths):
    script = '''
set -eu
base=https://pipe.u.isucon.dev
cookie=$(mktemp)
api(){ curl -kfSs --resolve pipe.u.isucon.dev:443:127.0.0.1 "$@"; }
api -c "$cookie" -H 'Content-Type: application/json' -d '{"username":"test001","password":"test"}' "$base/api/login"
'''
    for path in paths:
        script += 'api -b "$cookie" "$base"'+shlex.quote(path)+'\necho\n'
    return [json.loads(line) for line in ssh("s1","bash -s",script).splitlines() if line]


totals = '''WITH sc AS (
  SELECT l.id,l.user_id,COALESCE(r.n,0) reactions,COALESCE(c.tips,0) tips,
    COALESCE(c.n,0) comments,COALESCE(c.max_tip,0) max_tip,COALESCE(h.n,0) viewers
  FROM livestreams l
  LEFT JOIN (SELECT livestream_id,COUNT(*) n FROM reactions GROUP BY livestream_id) r ON r.livestream_id=l.id
  LEFT JOIN (SELECT livestream_id,COUNT(*) n,SUM(tip) tips,MAX(tip) max_tip FROM livecomments WHERE NOT moderated GROUP BY livestream_id) c ON c.livestream_id=l.id
  LEFT JOIN (SELECT livestream_id,COUNT(*) n FROM livestream_viewers_history GROUP BY livestream_id) h ON h.livestream_id=l.id
)'''
stream = query(totals+''', ranked AS (
  SELECT *,ROW_NUMBER() OVER(ORDER BY tips+reactions DESC,id DESC) pos FROM sc
) SELECT id,pos,reactions,max_tip,viewers,(SELECT COUNT(*) FROM livecomment_reports WHERE livestream_id=ranked.id)
  FROM ranked WHERE pos IN (1,2) ORDER BY pos''')
records = []
for row in stream.splitlines():
    stream_id,rank,reactions,maximum,views,reports=map(int,row.split())
    records.append((f"/api/livestream/{stream_id}/statistics",dict(rank=rank,total_reactions=reactions,
                    max_tip=maximum,viewers_count=views,total_reports=reports)))

users = query(totals+''', agg AS (
  SELECT u.id,u.name,COALESCE(SUM(sc.reactions),0) reactions,COALESCE(SUM(sc.tips),0) tips,
    COALESCE(SUM(sc.comments),0) comments,COALESCE(SUM(sc.viewers),0) views
  FROM users u LEFT JOIN sc ON sc.user_id=u.id GROUP BY u.id,u.name
), ranks AS (
  SELECT *,ROW_NUMBER() OVER(ORDER BY reactions+tips DESC,name DESC) pos FROM agg
) SELECT name,pos,reactions,tips,comments,views,
  COALESCE((SELECT r.emoji_name FROM reactions r JOIN livestreams l ON r.livestream_id=l.id
    WHERE l.user_id=ranks.id GROUP BY r.emoji_name ORDER BY COUNT(*) DESC,r.emoji_name DESC LIMIT 1),'')
  FROM ranks WHERE pos IN (1,2) OR name='test001' ORDER BY pos''')
for row in users.splitlines():
    cols=row.split("\t")
    name=cols[0]
    rank,reactions,tips,comments,views=map(int,cols[1:6])
    records.append((f"/api/user/{quote(name,safe='')}/statistics",dict(rank=rank,total_reactions=reactions,
                   total_tip=tips,total_livecomments=comments,viewers_count=views,favorite_emoji=cols[6] if len(cols)>6 else "")))
records.append(("/api/payment",{"total_tip":int(query("SELECT COALESCE(SUM(tip),0) FROM livecomments WHERE NOT moderated"))}))
actual=api([r[0] for r in records])
assert len(actual)==len(records)
for (path,expected),response in zip(records,actual):
    assert expected==response,(path,expected,response)
    print(path,response)
