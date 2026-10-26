#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""规范化 fpk 包内的文件权限位，并同步修复 manifest 的 checksum。

背景：
1. 在 Windows 上打包时 fnpack 无法保留可执行位（cmd/* 与 server/* 会变成 0666），
   安装后生命周期脚本可能无法执行；
2. manifest 中的 checksum 字段是 app.tgz 的 MD5，重写 app.tgz 后必须同步更新，
   否则安装校验会失败。

修正规则：
  cmd/*       -> 0755
  server/*    -> 0755
  目录         -> 0755
  其他文件     -> 0644
"""

import gzip
import hashlib
import io
import os
import sys
import tarfile


def rule_outer(name, is_dir):
    if is_dir:
        return 0o755
    if name.startswith("cmd/"):
        return 0o755
    return 0o644


def rule_inner(name, is_dir):
    if is_dir:
        return 0o755
    base = os.path.basename(name)
    if name.startswith("server/") or base == "main":
        return 0o755
    return 0o644


def normalize(tar_in, tar_out, rule):
    for m in tar_in.getmembers():
        ti = tarfile.TarInfo(m.name)
        ti.mtime = m.mtime
        ti.uid = 0
        ti.gid = 0
        ti.uname = ""
        ti.gname = ""
        ti.mode = rule(m.name, m.isdir())
        if m.isdir():
            ti.type = tarfile.DIRTYPE
            ti.size = 0
            tar_out.addfile(ti)
        elif m.issym():
            ti.type = tarfile.SYMTYPE
            ti.linkname = m.linkname
            tar_out.addfile(ti)
        else:
            f = tar_in.extractfile(m)
            data = f.read() if f else b""
            ti.type = tarfile.REGTYPE
            ti.size = len(data)
            tar_out.addfile(ti, io.BytesIO(data))


def rebuild_inner(blob):
    raw = gzip.decompress(blob)
    buf = io.BytesIO()
    with tarfile.open(fileobj=io.BytesIO(raw), mode="r:") as tin:
        with tarfile.open(fileobj=buf, mode="w", format=tarfile.GNU_FORMAT) as tout:
            normalize(tin, tout, rule_inner)
    return gzip.compress(buf.getvalue(), 6)


def patch_checksum(manifest_bytes, digest):
    try:
        text = manifest_bytes.decode("utf-8")
    except UnicodeDecodeError:
        return manifest_bytes
    lines = text.splitlines()
    out, found = [], False
    for line in lines:
        if line.strip().startswith("checksum"):
            out.append("%-22s= %s" % ("checksum", digest))
            found = True
        else:
            out.append(line)
    if not found:
        out.append("%-22s= %s" % ("checksum", digest))
    return ("\n".join(out) + "\n").encode("utf-8")


def main(path):
    blob = open(path, "rb").read()
    gz = blob[:2] == b"\x1f\x8b"
    raw = gzip.decompress(blob) if gz else blob

    new_inner = None
    inner_mtime = None
    entries = []
    with tarfile.open(fileobj=io.BytesIO(raw), mode="r:") as tin:
        for m in tin.getmembers():
            if m.name == "app.tgz" and m.isfile():
                inner_mtime = m.mtime
                new_inner = rebuild_inner(tin.extractfile(m).read())
            else:
                data = tin.extractfile(m).read() if m.isfile() else b""
                entries.append((m, data))

    if new_inner is None:
        print("!! app.tgz not found, nothing changed")
        return

    digest = hashlib.md5(new_inner).hexdigest()

    out = io.BytesIO()
    with tarfile.open(fileobj=out, mode="w", format=tarfile.GNU_FORMAT) as tout:
        ti = tarfile.TarInfo("app.tgz")
        ti.mode = 0o644
        ti.mtime = inner_mtime or 0
        ti.uid = ti.gid = 0
        ti.size = len(new_inner)
        tout.addfile(ti, io.BytesIO(new_inner))

        for m, data in entries:
            ti = tarfile.TarInfo(m.name)
            ti.mtime = m.mtime
            ti.uid = ti.gid = 0
            ti.uname = ti.gname = ""
            ti.mode = rule_outer(m.name, m.isdir())
            if m.isdir():
                ti.type = tarfile.DIRTYPE
                tout.addfile(ti)
            elif m.issym():
                ti.type = tarfile.SYMTYPE
                ti.linkname = m.linkname
                tout.addfile(ti)
            else:
                if m.name == "manifest":
                    data = patch_checksum(data, digest)
                ti.size = len(data)
                tout.addfile(ti, io.BytesIO(data))

    result = gzip.compress(out.getvalue(), 6) if gz else out.getvalue()
    tmp = path + ".tmp"
    with open(tmp, "wb") as f:
        f.write(result)
    os.replace(tmp, path)
    print("modes normalized, checksum ->", digest)


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("usage: fix_fpk_modes.py <file.fpk>")
        sys.exit(1)
    main(sys.argv[1])
