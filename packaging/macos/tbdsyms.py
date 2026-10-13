"""Prints "<install-name> <symbol>" for each library stub (.tbd) of an SDK: what a program
linked against that library can find in it, its inlined re-exported libraries included."""
import json
import os
import re
import sys

KEYS = r"(?:symbols|weak-symbols|thread-local-symbols|objc-classes|objc-eh-types|objc-ivars)"


def from_yaml(text):
    names, syms = [], set()
    for doc in re.split(r"(?m)^--- ", text):
        m = re.search(r"(?m)^install-name:\s*'?([^'\n]+?)'?\s*$", doc)
        if not m:
            continue
        names.append(m.group(1))
        for section in re.finditer(r"(?ms)^(?:exports|reexports):\n(.*?)(?=^\S|\Z)", doc):
            for lst in re.finditer(KEYS + r":\s*\[(.*?)\]", section.group(1), re.S):
                syms.update(s.strip("'\"") for s in re.split(r"[,\s]+", lst.group(1)) if s.strip("'\""))
    return names, syms


def from_json(text):
    d = json.loads(text)
    names, syms = [], set()
    for lib in [d.get("main_library", {})] + d.get("libraries", []):
        for n in lib.get("install_names", []):
            names.append(n["name"])
        for key in ("exported_symbols", "reexported_symbols"):
            for entry in lib.get(key, []):
                for part in ("data", "text"):
                    for kind, values in entry.get(part, {}).items():
                        prefix = {"objc_class": "_OBJC_CLASS_$_", "objc_eh_type": "_OBJC_EHTYPE_$_",
                                  "objc_ivar": "_OBJC_IVAR_$_"}.get(kind, "")
                        syms.update(prefix + v for v in values)
    return names, syms


out = set()
for root, _, files in os.walk(sys.argv[1]):
    for name in files:
        if name.endswith(".tbd"):
            text = open(os.path.join(root, name), errors="replace").read()
            names, syms = from_json(text) if text.lstrip().startswith("{") else from_yaml(text)
            if names:
                out.update(f"{names[0]} {s}" for s in syms)
for line in sorted(out):
    print(line)
