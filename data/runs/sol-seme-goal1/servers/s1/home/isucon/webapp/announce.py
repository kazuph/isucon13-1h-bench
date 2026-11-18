#!/usr/bin/env python3
"""Advertise the competition address after a reboot, including to idle peers."""
import ipaddress,json,socket,struct,subprocess,sys,time
address=str(ipaddress.IPv4Address(sys.argv[1]))
for attempt in range(60):
    interfaces=json.loads(subprocess.check_output(['ip','-j','-4','addr']))
    dev=next((v for v in interfaces if any(a['local']==address for a in v.get('addr_info',[]))),None)
    if dev: break
    time.sleep(1)
else: raise RuntimeError('competition address did not appear')
mac=bytes.fromhex(dev['address'].replace(':',''))
addr=socket.inet_aton(address)
frame=b'\xff'*6+mac+struct.pack('!H',0x0806)+struct.pack('!HHBBH',1,0x0800,6,4,1)+mac+addr+b'\x00'*6+addr
with socket.socket(socket.AF_PACKET,socket.SOCK_RAW,socket.htons(0x0806)) as sock:
    sock.bind((dev['ifname'],0))
    for repeat in range(5):
        sock.send(frame)
        time.sleep(0.2)
print(f'Advertised {address} on {dev["ifname"]} with {dev["address"]}')
