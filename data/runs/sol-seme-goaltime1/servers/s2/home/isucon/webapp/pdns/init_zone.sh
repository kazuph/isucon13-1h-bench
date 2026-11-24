#!/bin/bash
exec ssh -o BatchMode=yes s1 /home/isucon/webapp/pdns/init_zone.sh
