#!/usr/bin/env python3
"""Reproduce collation.bin using only the assigned s2 MySQL instance."""
import subprocess
from pathlib import Path
runes=[i for i in range(65536) if not 0xD800<=i<=0xDFFF]
raw=''.join(map(chr,runes)).encode().hex()
query="SELECT HEX(WEIGHT_STRING(_utf8mb4 0x"+raw+" COLLATE utf8mb4_general_ci));\n"
r=subprocess.run(['ssh','s2','sudo mysql -N -B'],input=query,text=True,capture_output=True,check=True)
b=bytes.fromhex(r.stdout.strip())
assert len(b)==len(runes)*2
out=bytearray(65536*2)
for j,i in enumerate(runes):out[2*i:2*i+2]=b[2*j:2*j+2]
Path(__file__).resolve().parents[1].joinpath('go/collation.bin').write_bytes(out)
