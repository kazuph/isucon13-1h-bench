#!/usr/bin/env python3
"""作業用 VM と同じイメージの中で、AI のファイル操作を順に流し直す。
- 外へ出るコマンド(ssh・rsync・bench など)は shims で何もしない。
- Claude Code の Bash は cd が次の呼び出しに残るので、各手順の終わりの作業フォルダを次へ引き継ぐ。
- AI が s1 から rsync した元のコード(orig/)は、公式の初期実装から先に置いておく。
"""
import json, os, shutil, subprocess, sys
steps = json.load(open(sys.argv[1], encoding="utf-8"))
work = "/home/isucon/work"
os.makedirs(work + "/orig", exist_ok=True)
for d in ("go", "sql", "pdns"):
    shutil.copytree(f"/orig_src/{d}", f"{work}/orig/{d}", dirs_exist_ok=True)
env = dict(os.environ, PATH="/replay/shims:" + os.environ["PATH"], HOME="/home/isucon")
cwd = work
log = open("/replay/replay.log", "w", encoding="utf-8")
for k, s in enumerate(steps):
    i = s["input"]
    if s.get("not_run"):
        log.write(f"[{k}] NOT_RUN_IN_ORIGINAL\n"); continue
    if s["tool"] == "Write":
        os.makedirs(os.path.dirname(i["file_path"]), exist_ok=True)
        open(i["file_path"], "w", encoding="utf-8").write(i["content"])
        log.write(f"[{k}] Write {i['file_path']}\n")
    elif s["tool"] == "Bash":
        if os.path.exists("/tmp/_cwd"):
            os.remove("/tmp/_cwd")
        script = f"cd {cwd!r} 2>/dev/null || cd {work!r}\n{i['command']}\n__rc=$?\npwd > /tmp/_cwd\nexit $__rc\n"
        try:
            r = subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True, timeout=60)
            log.write(f"[{k}] Bash rc={r.returncode} cwd={cwd} {i['command'][:100]!r}\n")
            if r.returncode != 0:
                log.write("    stderr: " + r.stderr.strip().replace("\n", " | ")[-400:] + "\n")
        except subprocess.TimeoutExpired:
            log.write(f"[{k}] Bash TIMEOUT {i['command'][:100]!r}\n")
        if i.get("run_in_background"):
            continue  # 裏で動かしたコマンドの cd は次へ残らない
        try:
            c = open("/tmp/_cwd").read().strip()
            if c.startswith(work) and os.path.isdir(c):
                cwd = c
        except FileNotFoundError:
            pass
    else:
        log.write(f"[{k}] SKIP {s['tool']}\n")
log.close()
