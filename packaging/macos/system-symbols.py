"""dyld on an older macOS refuses a program that needs a system library or symbol that macOS lacks.
The runners have a newer macOS than the oldest one the .pkg installs on, so the check goes by a
list made from the older macOS's SDK: one "# <install name>" line per library, then its symbols.

  system-symbols.py list SDK FOLDER > LIST    what that SDK's libraries, linked in FOLDER, have
  system-symbols.py check LIST.gz FOLDER      fails if FOLDER needs a library or symbol LIST lacks

Weak imports count too: the code calls them without checking that they exist."""
import gzip
import json
import os
import re
import subprocess
import sys

MACHO = (b"\xcf\xfa\xed\xfe", b"\xce\xfa\xed\xfe", b"\xca\xfe\xba\xbe")
TBD_KEYS = r"(?:symbols|weak-symbols|thread-local-symbols)"


def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True).stdout


def machos(folder):
    for root, _, files in os.walk(folder):
        for name in sorted(files):
            path = os.path.join(root, name)
            if not os.path.islink(path):
                with open(path, "rb") as f:
                    if f.read(4) in MACHO:
                        yield path


def system(name):
    return name.startswith(("/usr/lib/", "/System/"))


def linked(path):
    """The install names a Mach-O loads, by the short name nm -m gives them: the file name up to
    the first dot (/usr/lib/libSystem.B.dylib: libSystem), or the framework's name."""
    names = {}
    for line in run("otool", "-L", path).splitlines()[1:]:
        m = re.match(r"\s+(\S.*?) \(compatibility", line)
        if m:
            f = re.search(r"/([^/]+)\.framework/", m.group(1))
            names[f.group(1) if f else os.path.basename(m.group(1)).split(".")[0]] = m.group(1)
    return names


def tbd_symbols(text):
    """The C symbols a .tbd stub exports, those of the libraries it re-exports (inlined) included.
    Objective-C classes are left out: nothing in the runtime uses them, so the check fails if it does."""
    syms = set()
    if text.lstrip().startswith("{"):
        d = json.loads(text)
        for lib in [d.get("main_library", {})] + d.get("libraries", []):
            for key in ("exported_symbols", "reexported_symbols"):
                for entry in lib.get(key, []):
                    for part in ("data", "text"):
                        for kind in ("global", "weak", "thread_local"):
                            syms.update(entry.get(part, {}).get(kind, []))
        return syms
    for section in re.finditer(r"(?ms)^(?:exports|reexports):\n(.*?)(?=^\S|\Z)", text):
        for lst in re.finditer(TBD_KEYS + r":\s*\[(.*?)\]", section.group(1), re.S):
            syms.update(s.strip("'\"") for s in re.split(r"[,\s]+", lst.group(1)) if s.strip("'\""))
    return syms


def list_sdk(sdk, folder):
    names = sorted({n for p in machos(folder) for n in linked(p).values() if system(n)})
    for name in names:
        stub = os.path.join(sdk, name.lstrip("/"))
        stub = os.path.splitext(stub)[0] + ".tbd" if stub.endswith(".dylib") else stub + ".tbd"
        with open(stub, errors="replace") as f:
            text = f.read()
        if name not in text:
            sys.exit(f"{stub} is not the stub of {name}")
        print("# " + name)
        for s in sorted(tbd_symbols(text)):
            if s.startswith("_"):  # not the linker's $ld$ and R...$ entries
                print(s)


def check(listing, folder):
    libs, current = {}, None
    with gzip.open(listing, "rt") as f:
        for line in f:
            line = line.rstrip("\n")
            if line.startswith("# "):
                current = libs.setdefault(line[2:], set())
            elif line:
                current.add(line)
    problems, imports = [], 0
    for path in machos(folder):
        where, names = os.path.relpath(path, folder), linked(path)
        for line in run("nm", "-m", "-u", path).splitlines():
            m = re.match(r"\s*\(undefined\) (?:weak )?external (\S+) \(from (\S+)\)", line)
            if not m:
                if "(undefined)" in line:
                    problems.append(f"{where}: {line.strip()}")
                continue
            symbol, name = m.group(1), names.get(m.group(2), "")
            if not system(name):
                continue  # one of the folder's own libraries
            imports += 1
            if name not in libs:
                problems.append(f"{where}: links {name}, which the list does not have")
                libs[name] = None  # said once
            elif libs[name] is not None and symbol not in libs[name]:
                problems.append(f"{where}: {symbol} from {name}")
    for p in problems:
        print(p)
    print(f"{imports} imports from macOS checked, {len(problems)} not in {os.path.basename(listing)}")
    return 1 if problems else 0


if len(sys.argv) == 4 and sys.argv[1] == "list":
    list_sdk(sys.argv[2], sys.argv[3])
elif len(sys.argv) == 4 and sys.argv[1] == "check":
    sys.exit(check(sys.argv[2], sys.argv[3]))
else:
    sys.exit(__doc__)
