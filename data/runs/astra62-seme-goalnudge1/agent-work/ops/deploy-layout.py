#!/usr/bin/env python3
"""Install a measured revision and switch the active timeline and account roles."""
import argparse
import json
from pathlib import Path
import shlex
import subprocess
import tempfile
import time

HOSTS = ('s1', 's2', 's3')
IP = {host: f'10.99.0.{10+i}' for i, host in enumerate(HOSTS,1)}

def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)

def remote(host, command, **kwargs):
    return run('ssh', '-o','ConnectTimeout=5',host,command,**kwargs)

def put_text(host, path, content):
    remote(host, 'umask 077; cat > '+shlex.quote(path), input=content.encode())

def service(host, name, active):
    action='enable --now' if active else 'disable --now'
    remote(host,'sudo systemctl '+action+' '+name,stdout=subprocess.DEVNULL)
    if active:
        remote(host,'sudo systemctl restart '+name)

def await_ready(host, test):
    for _ in range(100):
        if subprocess.run(['ssh','-o','ConnectTimeout=2',host,test],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0:
            return
        time.sleep(.2)
    raise RuntimeError(f'{host} failed readiness check {test}')

def configure_database(config):
    enabled=config.get('db_binlog',True)
    flush=config.get('db_flush',1)
    assert isinstance(enabled,bool) and flush in (1,2)
    actual=remote('s3','sudo mysql -NBe "SELECT @@global.log_bin"',stdout=subprocess.PIPE).stdout.strip()==b'1'
    contents='[mysqld]\ninnodb_flush_log_at_trx_commit='+str(flush)+'\n'
    if not enabled:contents+='skip-log-bin\n'
    remote('s3','sudo tee /etc/mysql/mysql.conf.d/zzz-pipe-durability.cnf >/dev/null',input=contents.encode())
    if actual!=enabled:
        remote('s3','sudo systemctl restart mysql')
        await_ready('s3','sudo mysql -NBe "SELECT 1" >/dev/null')
        remote('s3','sudo systemctl start isupipe-go')
    remote('s3',f'sudo mysql -e "SET GLOBAL innodb_flush_log_at_trx_commit={flush}"')

def layout(revision, config, front):
    app = config['application']
    accounts = config.get('accounts','none')
    assert app in ('s1','s2') and accounts in ('none','s2','s3')
    for key in ('app_procs','app_gc','dns_procs'):
        value=config.get(key,2 if key.endswith('procs') else 100)
        assert isinstance(value,int) and 0 < value <= 2000
    rev=run('git','rev-parse','--verify',revision+'^{commit}',stdout=subprocess.PIPE,text=True).stdout.strip()
    use_pgo=bool(config.get('pgo',False))
    dest=f'/home/isucon/releases/{rev}'+('-pgo' if use_pgo else '')
    with tempfile.TemporaryDirectory() as tmp:
        tmp=Path(tmp)
        cached=all(subprocess.run(['ssh',host,f'cd {dest} && test -f verified.sha256 && sha256sum -c --status verified.sha256'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode==0 for host in HOSTS)
        if not cached:
            paths=['go']+(['ops/pgo/timeline.pgo'] if use_pgo else [])
            run('git','archive','-o',str(tmp/'source.tar'),rev,*paths)
            for host in HOSTS:
                remote(host,'mkdir -p '+dest)
                run('scp','-q',str(tmp/'source.tar'),host+':'+dest+'/source.tar')
                remote(host,'cd '+dest+' && tar xf source.tar')
            pgo='-pgo=../ops/pgo/timeline.pgo' if use_pgo else '-pgo=off'
            remote('s2',f'cd {dest}/go && ~/local/golang/bin/go build -trimpath {pgo} -o ../isupipe .')
            run('scp','-q','s2:'+dest+'/isupipe',str(tmp/'isupipe'))
            for host in ('s1','s3'):
                run('scp','-q',str(tmp/'isupipe'),host+':'+dest+'/isupipe.next')
                remote(host,f'mv {dest}/isupipe.next {dest}/isupipe')
            checksum=remote('s2',f'cd {dest} && sha256sum isupipe',stdout=subprocess.PIPE).stdout
            for host in HOSTS:
                remote(host,f'cd {dest} && cat >verified.sha256 && sha256sum -c --status verified.sha256',input=checksum)
        for host in HOSTS:
            remote(host,f'chmod 755 {dest}/isupipe; ln -sfn {dest} ~/pipe-current')
        for host in ('s2','s3'):
            run('scp','-q','ops/systemd/pipe-accounts.service',host+':/tmp/pipe-accounts.service')
            remote(host,'sudo install -m644 /tmp/pipe-accounts.service /etc/systemd/system/pipe-accounts.service')
        run('scp','-q','ops/systemd/pipe-local-app.service','s1:/tmp/pipe-local-app.service')
        remote('s1','sudo install -m644 /tmp/pipe-local-app.service /etc/systemd/system/pipe-local-app.service')
        # The private publication credential is already provisioned on the application host.
        # Copy it over SSH directly; never print it or add it to a deployment artifact.
        identity=remote('s2',"sed -n '/^ISUPIPE_ZONE_TOKEN=/p' ~/zone-client.env",stdout=subprocess.PIPE).stdout.decode()
        assert identity.startswith('ISUPIPE_ZONE_TOKEN=') and len(identity.strip())>40
        for host in ('s1','s2'):
            publish='no' if config.get('native_tls') else 'yes'
            put_text(host,'/home/isucon/zone-client.env',identity+f'ISUPIPE_ZONE_URL=http://10.99.0.11:8053\nISUPIPE_PUBLISH_ICONS={publish}\n')
            extra=f'GOMAXPROCS={config.get("app_procs",2)}\nGOGC={config.get("app_gc",100)}\n'
            group_us=config.get('group_us',0);assert isinstance(group_us,int) and -1<=group_us<=5000
            sync_mode=config.get('sync_mode','full');assert sync_mode in ('full','data','interval')
            sync_ms=config.get('sync_ms',5);assert isinstance(sync_ms,int) and 1<=sync_ms<=1000
            extra+=f'ISUPIPE_GROUP_US={group_us}\nISUPIPE_SYNC_MODE={sync_mode}\nISUPIPE_SYNC_MS={sync_ms}\n'
            extra+='ISUPIPE_SEARCH_CACHE='+('on' if config.get('search_cache',True) else 'off')+'\n'
            extra+='ISUPIPE_SEARCH_GZIP='+('yes' if config.get('search_gzip',False) else 'no')+'\n'
            if config.get('unix_socket') and host==app:
                extra+='ISUPIPE_UNIX_SOCKET=/home/isucon/webapp/api.sock\n'
            if config.get('native_tls') and host==app:
                assert app=='s1'
                target=IP[accounts]+':8090' if accounts!='none' else IP['s3']+':8080'
                remote('s1','sudo sh',input=Path('ops/install-app-tls.sh').read_bytes())
                extra+=f'ISUPIPE_TLS_ADDR=:443\nISUPIPE_TLS_FILES=/etc/isupipe/tls\nISUPIPE_LOGIN_URL=http://{target}\n'
            if accounts!='none':
                extra+=f'ISUPIPE_ACCOUNTS_URL=http://{IP[accounts]}:8090\n'
                if config.get('icon_worker'):extra+=f'ISUPIPE_ICON_WORKER=http://{IP[accounts]}:8090\n'
            put_text(host,'/home/isucon/pipe-app.env',extra)
        for host in ('s2','s3'):
            put_text(host,'/home/isucon/accounts.env',identity+f'ISUPIPE_ACCOUNTS_LISTEN={IP[host]}:8090\nISUPIPE_ZONE_URL=http://10.99.0.11:8053\n')
        remote('s1',"sed -i '/^ISUPIPE_DOMAIN_DB=/d; /^GOMAXPROCS=/d; /^ISUPIPE_DNS_WORKERS=/d; /^ISUPIPE_DNS_BATCH=/d; /^ISUPIPE_EDGE_ICONS=/d' ~/dns.env")
        workers=config.get('dns_workers',2);batch=config.get('dns_batch',16)
        assert isinstance(workers,int) and 1<=workers<=8 and isinstance(batch,int) and 1<=batch<=128
        remote('s1',f'echo ISUPIPE_DNS_WORKERS={workers} >> ~/dns.env; echo ISUPIPE_DNS_BATCH={batch} >> ~/dns.env')
        if not config.get('native_tls'):
            remote('s1','echo ISUPIPE_EDGE_ICONS=/var/lib/isupipe-icons >> ~/dns.env')
        zone_source=(run('git','show',rev+':go/domain.go',stdout=subprocess.PIPE,text=True).stdout)
        if 'ISUPIPE_DOMAIN_DB' in zone_source:
            remote('s1',"entry=$(sed -n 's/^ISUPIPE_EDGE_DB=/ISUPIPE_DOMAIN_DB=/p' ~/dns.env); printf '%s\\n' \"$entry\" >> ~/dns.env")
        remote('s1',f'echo GOMAXPROCS={config.get("dns_procs",2)} >> ~/dns.env')
        remote('s2',"printf '[Service]\\nEnvironmentFile=-/home/isucon/pipe-app.env\\n' | sudo tee /etc/systemd/system/isupipe-go.service.d/tuning.conf >/dev/null")
        integrated=config.get('integrated_dns',False)
        if integrated:
            assert app=='s1'
            remote('s1',"grep -vE '^(GOMAXPROCS|ISUPIPE_ZONE_TOKEN)=' ~/dns.env >> ~/pipe-app.env; echo ISUPIPE_INTEGRATED_DNS=yes >> ~/pipe-app.env")
        for host in HOSTS:remote(host,'sudo systemctl daemon-reload')
        # Never move a live journal. A cold copy preserves the preceding run even before initialize.
        state=Path('.git/pipe-journal-host')
        live=[host for host,unit in [('s1','pipe-local-app'),('s2','isupipe-go')] if subprocess.run(['ssh',host,'systemctl is-active --quiet '+unit]).returncode==0]
        if len(live)>1:raise RuntimeError('Two active timeline writers')
        old=live[0] if live else state.read_text().strip()
        assert old in ('s1','s2')
        remote(old,'sudo systemctl stop '+('pipe-local-app' if old=='s1' else 'isupipe-go'))
        if app!=old:
            run('scp','-q',old+':webapp/activity.events',str(tmp/'activity.events'))
            run('scp','-q',str(tmp/'activity.events'),app+':webapp/activity.events.next')
            remote(app,'mv ~/webapp/activity.events.next ~/webapp/activity.events')
        state.write_text(app)
        configure_database(config)
        for host in ('s1','s2'):
            remote(host, f'install {dest}/isupipe ~/webapp/go/isupipe.next && mv ~/webapp/go/isupipe.next ~/webapp/go/isupipe && cp {dest}/go/*.go ~/webapp/go/')
        service('s1','isupipe-go',not integrated)
        if not integrated:await_ready('s1','ss -lnt | grep -q :8053')
        for host in ('s2','s3'):service(host,'pipe-accounts',host==accounts)
        if accounts!='none':await_ready(accounts,'ss -lnt | grep -q :8090')
        # Retain public s1 addressing, independent of the active internal roles.
        import re
        front=re.sub(r'(upstream pipe_api \{ server )[0-9.:]+',lambda m:m[1]+IP[app]+':8080',front)
        if config.get('unix_socket') and app=='s1':
            front=front.replace('upstream pipe_api { server '+IP[app]+':8080','upstream pipe_api { server unix:/home/isucon/webapp/api.sock')
        auth=IP[accounts]+':8090' if accounts!='none' else IP['s3']+':8080'
        front=re.sub(r'(upstream pipe_login \{ server )[0-9.:]+',lambda m:m[1]+auth,front)
        if config.get('native_tls'):
            front=front.replace('listen 443 ssl default_server;', 'listen 127.0.0.1:8085 default_server;').replace('listen 443 ssl http2;', 'listen 127.0.0.1:8085;').replace('listen 443 ssl;', 'listen 127.0.0.1:8085;')
            # The native listener owns TLS while nginx continues to serve static files.
        ngworkers=config.get('nginx_workers',3);assert isinstance(ngworkers,int) and 1<=ngworkers<=8
        remote('s1',f"sudo sed -i -E 's/^worker_processes [^;]+;/worker_processes {ngworkers};/' /etc/nginx/nginx.conf")
        put_text('s1','/tmp/pipe-layout-front.conf',front)
        remote('s1','sudo cp /tmp/pipe-layout-front.conf /etc/nginx/sites-enabled/isupipe.conf && sudo nginx -t && sudo systemctl reload nginx')
        service('s1','pipe-local-app',app=='s1')
        service('s2','isupipe-go',app=='s2')
        await_ready(app,'curl -fsS localhost:8080/api/tag >/dev/null')
        if integrated:await_ready('s1','ss -lnt | grep -q :8053')
        await_ready('s1','curl -kfsS --resolve pipe.u.isucon.dev:443:127.0.0.1 https://pipe.u.isucon.dev/api/tag >/dev/null')
    return front

if __name__=='__main__':
    parser=argparse.ArgumentParser()
    parser.add_argument('revision');parser.add_argument('layout',type=Path);parser.add_argument('front',type=Path)
    parser.add_argument('--save-front',type=Path)
    args=parser.parse_args();front=layout(args.revision,json.loads(args.layout.read_text()),args.front.read_text())
    if args.save_front:args.save_front.write_text(front)
