"""Exercise moderation, retained reports, and the reference SQL collation."""
import json
import subprocess
import tempfile
import time
from pathlib import Path

stream=subprocess.check_output(['ssh','s2','sudo mysql -BN isupipe -e "SELECT MIN(id) FROM livestreams WHERE user_id=(SELECT id FROM users WHERE name=\'test001\')"'],text=True).strip()
base="https://pipe.u.isucon.dev"
prefix="/api/livestream/"+stream
with tempfile.TemporaryDirectory() as work:
    cookie=Path(work)/"cookie"
    def request(path,payload=None):
        output=Path(work)/"response"
        args=['curl','--noproxy','*','-ksS','--max-time','8','--resolve','pipe.u.isucon.dev:443:10.99.0.11',
              '-b',str(cookie),'-c',str(cookie),'-o',str(output),'-w','%{http_code}',base+path]
        if payload is not None:
            args+=['-H','Content-Type: application/json','--data-binary','@-']
        r=subprocess.run(args,input=json.dumps(payload,ensure_ascii=False),text=True,capture_output=True,check=True)
        raw=output.read_text()
        return int(r.stdout),json.loads(raw) if raw else None
    assert request('/api/login',{'username':'test001','password':'test'})[0]==200
    marker=str(time.time_ns())
    message='ordinary café'+marker
    status,comment=request(prefix+'/livecomment',{'comment':message,'tip':0})
    assert status==201,(status,comment)
    assert request(prefix+'/livecomment/'+str(comment['id'])+'/report',{})[0]==201
    assert request(prefix+'/moderate',{'ng_word':'CAFÉ'+marker})[0]==201
    assert request(prefix+'/livecomment',{'comment':'ordinary cafe'+marker,'tip':0})[0]==400
    status,visible=request(prefix+'/livecomment')
    assert status==200 and all(item['id']!=comment['id'] for item in visible)
    status,reports=request(prefix+'/report')
    assert status==200 and any(item['livecomment']['id']==comment['id'] for item in reports)
    assert request(prefix+'/moderate',{'ng_word':'field_'+marker})[0]==201
    assert request(prefix+'/livecomment',{'comment':'fieldX'+marker,'tip':0})[0]==400
    assert request(prefix+'/livecomment',{'comment':'allowed'+marker,'tip':0})[0]==201
print('Verified accent/case matching, SQL wildcard matching, removal from the timeline, and retained report data.')
