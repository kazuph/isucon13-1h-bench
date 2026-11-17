import sys,re,collections
st=collections.defaultdict(lambda:[0,0.0,0.0,0,collections.Counter()])
for l in open(sys.argv[1]):
    p=l.split()[2:]
    if len(p)<7: continue
    m,u,s,rt=p[0],p[1].split("?")[0],p[2],float(p[3])
    u=re.sub(r'/\d+','/:id',u); u=re.sub(r'/api/user/[^/]+','/api/user/:u',u) if not u.startswith('/api/user/me') else u
    k=m+' '+u; e=st[k]; e[0]+=1; e[1]+=rt; e[2]=max(e[2],rt); e[3]+=int(p[5]) if p[5].isdigit() else 0; e[4][s]+=1
for k,v in sorted(st.items(),key=lambda x:-x[1][1]):
    print(f"{v[0]:7d} sum={v[1]:8.1f} avg={v[1]/v[0]*1000:7.1f}ms max={v[2]:.2f} bytes={v[3]//max(v[0],1):7d} {k} {dict(v[4])}")
