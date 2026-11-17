#!/usr/bin/env python3
import json,subprocess,time,urllib.request
hosts={'s1':['nginx','isupipe-dns','isupipe-announce'],'s2':['mysql','isupipe-memory','isupipe-announce'],'s3':['isupipe-crypto','isupipe-announce']}
start=time.monotonic()
for attempt in range(60):
    verified={}
    for host,units in hosts.items():
        try:
            result=subprocess.run(['ssh','-o','ConnectTimeout=2',host,'systemctl is-active '+' '.join(units)+'; cat /proc/sys/kernel/random/boot_id; sha256sum /home/isucon/webapp/go/isupipe-memory'],capture_output=True,text=True,timeout=3)
            lines=result.stdout.splitlines()
            if result.returncode==0 and lines[:len(units)]==['active']*len(units): verified[host]={'services':units,'boot_id':lines[len(units)],'binary_sha256':lines[len(units)+1].split()[0]}
        except (subprocess.TimeoutExpired,IndexError): pass
    if len(verified)==len(hosts):
        try:
            with urllib.request.urlopen('http://10.99.0.12:8080/api/tag',timeout=2) as r:
                if r.status==200:
                    assert len({v['binary_sha256'] for v in verified.values()})==1,'binaries differ'
                    print(json.dumps({'ready_after_s':round(time.monotonic()-start,2),'hosts':verified},ensure_ascii=False));break
        except OSError: pass
    time.sleep(1)
else: raise RuntimeError('services failed to start after reboot')
