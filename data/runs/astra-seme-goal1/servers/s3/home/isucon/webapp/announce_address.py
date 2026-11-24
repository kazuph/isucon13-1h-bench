#!/usr/bin/env python3
"""Announce this server's assigned address after boot/MAC changes (RFC 5227)."""
import pathlib,socket,struct,subprocess,sys,time
address=sys.argv[1]
assert address in ('10.99.0.11','10.99.0.12','10.99.0.13')
interface='eth0'
for _ in range(60):
    if f'inet {address}/' in subprocess.check_output(['ip','-4','addr','show','dev',interface],text=True):break
    time.sleep(.5)
else:raise SystemExit('Assigned address is not present; refusing announcement')
mac=bytes.fromhex(pathlib.Path('/sys/class/net/'+interface+'/address').read_text().strip().replace(':',''))
ip=socket.inet_aton(address)
# A gratuitous ARP request: sender and target protocol address are our own IP.
packet=b'\xff'*6+mac+struct.pack('!H',0x0806)+struct.pack('!HHBBH',1,0x0800,6,4,1)+mac+ip+b'\x00'*6+ip
with socket.socket(socket.AF_PACKET,socket.SOCK_RAW,socket.htons(0x0806)) as sock:
    sock.bind((interface,0))
    for _ in range(3):sock.send(packet);time.sleep(.2)
print('Announced',address,'with MAC',mac.hex(':'),flush=True)
