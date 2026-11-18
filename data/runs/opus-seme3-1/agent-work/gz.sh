#!/bin/bash
# gz.sh on|off
if [ "$1" = on ]; then ssh s1 'sudo sed -i "s/^\t#X gzip_types/\tgzip_types/" /etc/nginx/nginx.conf; sudo systemctl reload nginx'
else ssh s1 'sudo sed -i "s/^\tgzip_types/\t#X gzip_types/" /etc/nginx/nginx.conf; sudo systemctl reload nginx'; fi
ssh s1 'grep "gzip_types" /etc/nginx/nginx.conf | head -2'
