#!/usr/bin/env python3
"""Print the commit a checkout's HEAD points at, read from its .git directory (no VCS binary needed).

Usage: python3 -I headrev.py <checkout dir>. Prints "unknown" when it cannot be resolved."""
import os
import sys


def gitdir(repo):
    d = os.path.join(repo, ".git")
    if os.path.isfile(d):  # a worktree or submodule: "gitdir: <path>"
        with open(d) as f:
            line = f.read().strip()
        if line.startswith("gitdir:"):
            d = os.path.normpath(os.path.join(repo, line.split(":", 1)[1].strip()))
    return d


def resolve(d, ref):
    common = d
    cd = os.path.join(d, "commondir")
    if os.path.isfile(cd):
        with open(cd) as f:
            common = os.path.normpath(os.path.join(d, f.read().strip()))
    for base in (d, common):
        p = os.path.join(base, ref)
        if os.path.isfile(p):
            with open(p) as f:
                return f.read().strip()
    packed = os.path.join(common, "packed-refs")
    if os.path.isfile(packed):
        with open(packed) as f:
            for line in f:
                parts = line.split()
                if len(parts) == 2 and parts[1] == ref:
                    return parts[0]
    return "unknown"


def main():
    d = gitdir(sys.argv[1])
    try:
        with open(os.path.join(d, "HEAD")) as f:
            head = f.read().strip()
    except OSError:
        print("unknown")
        return
    if head.startswith("ref:"):
        ref = head.split(":", 1)[1].strip()
        print(f"{resolve(d, ref)} ({ref.removeprefix('refs/heads/')})")
    else:
        print(head)


if __name__ == "__main__":
    main()
