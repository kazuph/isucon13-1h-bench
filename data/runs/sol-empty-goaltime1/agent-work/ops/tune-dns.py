from pathlib import Path
p=Path('/etc/powerdns/pdns.conf')
s=p.read_text()
for old,new in [('log-dns-queries=yes','log-dns-queries=no'),('log-dns-details=yes','log-dns-details=no'),('loglevel=7','loglevel=3'),('query-cache-ttl=0','query-cache-ttl=20')]:
    s="\n".join(new if line == old else line for line in s.split("\n"))
p.write_text(s)
