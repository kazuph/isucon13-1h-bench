#!/usr/bin/env python3
import json,pathlib,random,socket,struct
root=pathlib.Path(__file__).resolve().parents[1]
name=json.loads((root/'logs/persistence.json').read_text())['name']+'.u.isucon.dev'
for domain in ['pipe.u.isucon.dev',name,'not-registered-verification.invalid.u.isucon.dev']:
    ident=random.randrange(65536)
    question=b''.join(bytes([len(p)])+p.encode() for p in domain.split('.'))+b'\0'+struct.pack('!HH',1,1)
    request=struct.pack('!HHHHHH',ident,256,1,0,0,0)+question
    with socket.socket(socket.AF_INET,socket.SOCK_DGRAM) as s:
        s.settimeout(3);s.sendto(request,('10.99.0.11',53));reply,_=s.recvfrom(4096)
    rid,flags,qn,an,_,_=struct.unpack('!HHHHHH',reply[:12]);assert rid==ident
    if domain.startswith('not-registered'):
        assert flags&15==3,(domain,flags)
    else:
        assert flags&15==0 and an>=1,(domain,flags,an)
        assert reply[-4:]==bytes([10,99,0,11]),reply
    print('PASS DNS:',domain)
