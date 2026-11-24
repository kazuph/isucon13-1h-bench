"""Compare service configurations using the competition benchmark, one run at a time."""
import datetime as dt
import json
from pathlib import Path
import random
import statistics
import subprocess
import time

ROOT = Path(__file__).resolve().parent
RUNS = ROOT / "runs"
RUNS.mkdir(exist_ok=True)
END = dt.datetime.fromisoformat("2026-11-21T07:43:00+00:00").timestamp()
DEFAULT = {"app_host": "s3", "gomax": 2, "gc": 100, "pool": 32, "workers": 2, "gzip": 0, "dns": "original", "http2": 0, "buffer": 1, "connections": 64, "accelerate": 0, "batch": 32}
DOMAINS = {"app_host": ["s2","s3"], "gomax": [1, 2, 3, 4], "gc": [50, 100, 200, 400],
           "pool": [8, 16, 32, 64, 96], "workers": [1, 2, 3],
           "gzip": [0, 1], "dns": ["original","direct","reuse2"],
           "http2": [0,1], "buffer": [0,1], "connections": [16,64,256], "accelerate": [0,1], "batch": [1,8,32,64]}
randomizer = random.Random(513)


def ssh(host, command, data=None):
    return subprocess.run(["ssh", "-o", "ConnectTimeout=8", host, command],
                          input=data, text=True, check=True, capture_output=True,
                          timeout=60).stdout


def configure(config, previous):
    app_host = config["app_host"]
    if app_host not in ("s2", "s3"):
        raise ValueError("Unknown application server")
    if previous.get("app_host") != app_host:
        idle_host = "s3" if app_host == "s2" else "s2"
        ssh(idle_host, "sudo systemctl disable --now isupipe-go")
        ssh(app_host, "sudo systemctl enable isupipe-go")
    if any(config[key] != previous.get(key) for key in ("app_host", "gomax", "gc", "pool", "batch")):
        dropin = ("[Service]\nEnvironment=GOMAXPROCS={gomax}\n"
                  "Environment=GOGC={gc}\nEnvironment=ISUPIPE_DB_POOL={pool}\nEnvironment=ISUPIPE_WRITE_BATCH={batch}\n").format(**config)
        ssh(app_host, "sudo tee /etc/systemd/system/isupipe-go.service.d/performance.conf >/dev/null"
            " && sudo systemctl daemon-reload && sudo systemctl restart isupipe-go"
            " && curl -fsS --retry 10 --retry-connrefused --retry-delay 1 localhost:8080/api/tag >/dev/null", dropin)
    if any(config[k] != previous.get(k) for k in ("dns","accelerate")):
        profiles = {"original": (1,3,"no"),"direct": (1,1,"no"),"reuse2": (2,1,"yes")}
        receivers,distributors,reuse = profiles[config["dns"]]
        conf = (ROOT.parent / "webapp/pdns/pdns.conf").read_text()
        conf += f"\nreceiver-threads={receivers}\ndistributor-threads={distributors}\nreuseport={reuse}\n"
        edge_listen = "0.0.0.0:53" if config["accelerate"] else ""
        if edge_listen:
            conf=conf.replace("local-port=53\n","local-port=5353\n")+"local-address=127.0.0.1\n"
        dropin = "[Service]\nAmbientCapabilities=CAP_NET_BIND_SERVICE\nEnvironment=ZONE_DNS_LISTEN="+edge_listen+"\nEnvironment=ZONE_DNS_BACKEND=127.0.0.1:5353\n"
        ssh("s1","sudo mkdir -p /etc/systemd/system/zone-control.service.d"
            " && sudo tee /etc/systemd/system/zone-control.service.d/listen.conf >/dev/null", dropin)
        ssh("s1", "sudo tee /etc/powerdns/pdns.conf >/dev/null && sudo pdns_server --config=check"
            " && sudo systemctl stop zone-control && sudo systemctl daemon-reload"
            " && sudo systemctl restart pdns && sudo systemctl start zone-control"
            " && for i in 1 2 3 4 5; do test \"$(dig +short +time=1 +tries=1 @10.99.0.11 pipe.u.isucon.dev)\" = 10.99.0.11 && exit 0; sleep 1; done; exit 1", conf)
    if any(config[key] != previous.get(key) for key in ("app_host","workers","gzip","http2","buffer","connections")):
        workers = config["workers"]
        gzip = "gzip_types application/json;\ngzip_comp_level 1;\n" if config["gzip"] else ""
        conf = (ROOT.parent / "webapp/nginx/isupipe.conf").read_text()
        if app_host == "s2":
            conf = conf.replace("server 10.99.0.13:8080;", "server 10.99.0.12:8080;")
        if config["http2"]:
            conf = conf.replace("listen 443 ssl", "listen 443 ssl http2")
        if not config["buffer"]:
            conf = conf.replace("location /api {","location /api {\n    proxy_buffering off;")
        conf = conf.replace("keepalive 64;", f"keepalive {config['connections']};")
        ssh("s1", "sudo tee /etc/nginx/sites-enabled/isupipe.conf >/dev/null",conf)
        ssh("s1", "sudo tee /etc/nginx/conf.d/isupipe-compression.conf >/dev/null"
            f" && sudo sed -i -E 's/^worker_processes [^;]+;/worker_processes {workers};/' /etc/nginx/nginx.conf"
            " && sudo nginx -t && sudo systemctl reload nginx", gzip)



def one(config, label):
    stamp = time.time_ns()
    result = subprocess.run(["bench"], capture_output=True, text=True)
    raw = result.stdout.strip()
    try:
        data = json.loads(raw)
    except ValueError:
        data = {"pass": False, "score": 0, "diagnostic": (raw + result.stderr)[-4000:]}
    row = dict(config=config, label=label, benchmark=data)
    (RUNS / f"{stamp}.json").write_text(json.dumps(row, ensure_ascii=False))
    print(f"{label} score={data.get('score', 0)} pass={data.get('pass')} {config}", flush=True)
    return data.get("score", 0) if data.get("pass") else 0


def hold_requested():
    return (ROOT / "hold").exists()


def evaluate(config, previous, label):
    configure(config, previous)
    previous.update(config)
    return one(config, label)


def main():
    previous = {}
    checkpoint = ROOT / "best.json"
    best = DEFAULT.copy()
    if checkpoint.exists(): best.update(json.loads(checkpoint.read_text()))
    search_end = END - 14*60
    tested = {}
    round_number = 0
    while time.time() < search_end:
        round_number += 1
        keys=list(DOMAINS)
        randomizer.shuffle(keys)
        if round_number==1: keys=["app_host","accelerate"]+[k for k in keys if k not in ("app_host","accelerate")]
        for key in keys:
            if time.time() > search_end: break
            while hold_requested() and time.time() < search_end:
                (ROOT / "held").touch()
                time.sleep(1)
            (ROOT / "held").unlink(missing_ok=True)
            if time.time() >= search_end: break
            available = [v for v in DOMAINS[key] if v != best[key]]
            counts = tested.setdefault(key, {})
            least = min(counts.get(v, 0) for v in available)
            alternative = randomizer.choice([v for v in available if counts.get(v, 0) == least])
            counts[alternative] = counts.get(alternative, 0) + 1
            challenger = dict(best, **{key: alternative})
            results = {"incumbent": [], "candidate": []}
            # Paired comparisons reduce the effect of other loads on the shared host.
            for label in ["incumbent","candidate","candidate","incumbent"]:
                if time.time() >= search_end or hold_requested(): break
                config = best if label == "incumbent" else challenger
                score = evaluate(config, previous, f"{round_number}-{key}-{label}")
                results[label].append(score)
                if not score and label=="candidate": break
            if all(len(s)==2 for s in results.values()):
                incumbent, candidate = results["incumbent"], results["candidate"]
                if min(candidate)>0 and min(incumbent)>0 and statistics.mean(candidate)>statistics.mean(incumbent)*1.025:
                    best = challenger
                    checkpoint.write_text(json.dumps(best))
            configure(best, previous)
            previous.update(best)
    configure(best,{})
    checkpoint.write_text(json.dumps(best))
    print("settled best configuration",best,flush=True)


if __name__=="__main__":
    main()
