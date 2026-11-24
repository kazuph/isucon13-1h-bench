import sys

configuration = sys.stdin.read()
configuration = configuration.replace(
    "  client_max_body_size 10m;",
    "  client_max_body_size 10m;\n"
    "  ssl_session_cache shared:isupipe_tls:16m;\n"
    "  access_log off;",
)
configuration = configuration.replace(
    "  location /api {",
    "  location = /api/initialize {\n"
    "    proxy_set_header Host $host;\n"
    "    proxy_pass http://127.0.0.1:8080;\n"
    "  }\n"
    "  location /api {\n"
    "    proxy_http_version 1.1;\n"
    '    proxy_set_header Connection "";',
)
sys.stdout.write(configuration.replace("http://localhost:8080", "http://isupipe_workers"))
