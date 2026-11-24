from pathlib import Path
s=Path('checks/verify.py').read_text().replace('import http.client,json,subprocess,hashlib','import http.client,json,subprocess,hashlib,shlex')
s=s.replace("['mysql','-h10.99.0.13','-uisucon','-pisucon','-N','-B','isupipe','-e',q]","['ssh','s3','sudo mysql -N -B isupipe -e '+shlex.quote(q)]")
s=s.replace("'127.0.0.1',8080","'127.0.0.1',18080")
exec(compile(s,'verify.py','exec'))
