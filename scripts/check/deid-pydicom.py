"""Check de-identified files against their originals with pydicom.

Input: the path of a JSON file:
  {"keywords": [<DICOM keywords whose values the profile removes or replaces>],
   "files": [{"original": <path of a file as received>,
              "deidentified": <path of the same file de-identified>}]}

For each pair, pydicom reads the original and gathers the values of every
element named by a keyword, at any depth of sequences: the whole value,
each value of a multi-valued one, and each part of a person's name. Values
shorter than 4 characters are skipped (they match inside ordinary text),
and so are UIDs the standard registers (a SOP class stays in every file).

pydicom then reads the de-identified file and searches every element of it,
the file meta information, private elements and every item of every
sequence included, Pixel Data excepted: each text value as pydicom decodes
it, and each binary value read as Latin-1, for every gathered value,
ignoring case. A value found is printed with the element it was found in
(the value itself is synthetic test data). The file must also say Patient
Identity Removed (0012,0062) YES.

Exit status 0 when no value is found in any file and at least one value was
searched for; 1 otherwise.
"""
import json
import sys

import pydicom
from pydicom.uid import UID

MIN_LENGTH = 4
PIXEL_TAGS = {0x7FE00008, 0x7FE00009, 0x7FE00010}


def values_of(elem):
    """The text forms of an element's value."""
    v = elem.value
    items = list(v) if isinstance(v, (list, pydicom.multival.MultiValue)) else [v]
    out = []
    for x in items:
        if x is None:
            continue
        if elem.VR == "PN":
            s = str(x)
            out.append(s)
            for group in s.split("="):
                out.extend(group.split("^"))
        elif isinstance(x, bytes):
            out.append(x.decode("latin-1"))
        else:
            out.append(str(x))
    if len(items) > 1:
        out.append("\\".join(str(x) for x in items))
    return out


def needles(ds, keywords):
    found = set()
    for elem in ds.iterall():
        if elem.keyword not in keywords or elem.VR == "SQ":
            continue
        for s in values_of(elem):
            s = s.strip(" \x00")
            if len(s) < MIN_LENGTH:
                continue
            if elem.VR == "UI" and UID(s).is_valid and UID(s).name != s:
                continue
            found.add(s)
    return found


def texts(ds):
    """Every element's value as text, with where it is."""
    out = []
    elems = list(ds.file_meta.iterall()) if hasattr(ds, "file_meta") else []
    elems += list(ds.iterall())
    for elem in elems:
        if elem.tag in PIXEL_TAGS or elem.VR == "SQ":
            continue
        try:
            for s in values_of(elem):
                out.append((str(elem.tag), s))
        except Exception:  # a value pydicom cannot convert is read as bytes
            raw = getattr(elem, "value", b"")
            out.append((str(elem.tag), repr(raw)))
    return out


def main():
    with open(sys.argv[1], encoding="utf-8") as f:
        spec = json.load(f)
    keywords = set(spec["keywords"])
    searched = 0
    bad = 0
    for pair in spec["files"]:
        orig = pydicom.dcmread(pair["original"], force=True)
        out = pydicom.dcmread(pair["deidentified"])
        if str(out.get("PatientIdentityRemoved", "")) != "YES":
            print(f"{pair['deidentified']}: Patient Identity Removed is not YES")
            bad += 1
        wanted = needles(orig, keywords)
        searched += len(wanted)
        hay = texts(out)
        for n in sorted(wanted):
            low = n.lower()
            for where, s in hay:
                if low in s.lower():
                    print(f"{pair['original']}: {n!r} found in {where} of the de-identified file")
                    bad += 1
    print(f"{len(spec['files'])} files, {searched} values searched for, {bad} found")
    if bad or searched == 0:
        sys.exit(1)


if __name__ == "__main__":
    main()
