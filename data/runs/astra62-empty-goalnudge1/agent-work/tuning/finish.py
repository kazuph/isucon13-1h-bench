"""Verify the settled configuration, data persistence and reboot reproducibility before cutoff."""
import concurrent.futures
import json
from pathlib import Path
import subprocess
import sys
import time

from measure import DEFAULT, END, ROOT, one, ssh

def snapshot():
    sql = """
        SELECT COUNT(*),COALESCE(SUM(tip),0),COALESCE(MAX(id),0) FROM livecomments WHERE NOT moderated;
        SELECT COUNT(*),COALESCE(MAX(id),0) FROM reactions;
        SELECT COUNT(*),COALESCE(MAX(id),0) FROM users;
        SELECT COUNT(*),COALESCE(MAX(id),0) FROM livestreams;
        SELECT COUNT(*),COALESCE(MAX(id),0),COALESCE(BIT_XOR(CRC32(image_hash)),0) FROM icons;
        SELECT COUNT(*),COALESCE(SUM(OCTET_LENGTH(image)),0) FROM icon_objects;
        SELECT COUNT(*) FROM livecomment_reports;
        SELECT COUNT(*) FROM ng_words;
    """
    return ssh('s2','sudo mysql -BN isupipe',sql)

def boot(host):
    return ssh(host,'cat /proc/sys/kernel/random/boot_id').strip()

def assert_ready():
    ssh('s1','systemctl is-active --quiet nginx && systemctl is-active --quiet pdns && systemctl is-active --quiet zone-control'
        ' && test "$(dig +short @10.99.0.11 pipe.u.isucon.dev)" = 10.99.0.11'
        ' && curl -kfSs --resolve pipe.u.isucon.dev:443:127.0.0.1 https://pipe.u.isucon.dev/api/tag >/dev/null')
    ssh('s2','systemctl is-active --quiet mysql')
    best = json.loads((ROOT/'best.json').read_text())
    ssh(best.get('app_host','s3'),'systemctl is-active --quiet isupipe-go')
    subprocess.run([sys.executable,str(ROOT/'verify_stats.py')],check=True)

def restore_connectivity():
    # ARP announcements can lag behind the virtual hosts' simultaneous reboot.
    for via in ('s3','s1','s2'):
        for target in ('s1','s2','s3'):
            if target==via: continue
            cmd=['ssh','-o','ConnectTimeout=2','-o','BatchMode=yes','-J',via,target,'ping -c 1 -W 1 10.99.0.100 >/dev/null']
            try: subprocess.run(cmd,capture_output=True,timeout=6)
            except subprocess.TimeoutExpired: pass

def main():
    if time.time() < END-20*60 or time.time() >= END-8*60:
        raise SystemExit("Reboot verification starts in the reserved final window.")
    hosts=('s1','s2','s3')
    before={h:boot(h) for h in hosts}
    saved=snapshot()
    assert_ready()
    with concurrent.futures.ThreadPoolExecutor(max_workers=3) as executor:
        jobs={host:executor.submit(subprocess.run,['ssh',host,'sudo reboot'],capture_output=True,timeout=15) for host in hosts}
        for job in jobs.values():
            try: job.result()
            except subprocess.TimeoutExpired: pass
    after={}
    for attempt in range(30):
        if time.time()>END-6*60:break
        time.sleep(4)
        for host in hosts:
            if host in after:continue
            try:
                current=subprocess.run(['ssh','-o','ConnectTimeout=2',host,'cat /proc/sys/kernel/random/boot_id'],capture_output=True,text=True,timeout=4)
                if current.returncode==0 and current.stdout.strip()!=before[host]:
                    after[host]=current.stdout.strip()
            except subprocess.TimeoutExpired:pass
        if len(after)==3:break
        if attempt in (2,6):restore_connectivity()
    if len(after)!=3:raise RuntimeError("Missing reboot evidence: "+str(after))
    for attempt in range(12):
        try:assert_ready();break
        except subprocess.SubprocessError:
            if attempt==11:raise
            time.sleep(3)
    restored=snapshot()
    if restored!=saved:raise RuntimeError("Data changed across restart")
    metadata={'boot_before':before,'boot_after':after,'snapshot':restored}
    (ROOT/'reboot-final.json').write_text(json.dumps(metadata))
    conf=DEFAULT.copy()
    best=ROOT/'best.json'
    if best.exists():conf.update(json.loads(best.read_text()))
    scores=[]
    for i in range(3):
        if time.time()>END-100:break
        scores.append(one(conf,'final-reproduction-'+str(i+1)))
    (ROOT/'final-checks.json').write_text(json.dumps(scores))
    if len(scores)<2 or min(scores)<=0 or min(scores)<=max(scores)*.75:
        raise RuntimeError("Reproduction needs review: "+str(scores))
    print("Reproduced and verified",scores,flush=True)

if __name__=="__main__":main()
