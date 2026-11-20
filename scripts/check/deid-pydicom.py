"""Check de-identified files against their originals with pydicom.

Input: the path of a JSON file:
  {"keywords": [<DICOM keywords whose values must be nowhere in a result>],
   "files": [{"original": <path of a file as received>,
              "deidentified": <path of the same file de-identified>,
              "needles": <path of a JSON list of the values the engine
                          removed or replaced: {"kind": "name" | "date" |
                          "uid" | "text", "value": ...}>}]}

The values searched for in each result, its needles, come from three
sources, so a mistake in one is caught by another:

  - the engine's own needles for the file;
  - pydicom's reading of the original: the value of every element named by
    a keyword, and of every element PS3.15 Table E.1-1 removes or replaces
    whatever a profile says (FLOOR below: the names, identifiers, birth
    date and time, addresses and telephone numbers of persons), and of
    every element of VR PN, at any depth of sequences;
  - the values of the original's private elements, which a profile removes
    unless a rule keeps them, when they are text and appear in no standard
    element of the original (a value that does may be kept legitimately).

Each needle is searched for in several written forms: a person's name as
DOE^JOHN, JOHN DOE, DOE JOHN, DOE, JOHN and DOE,JOHN, and each part; a date
as 20240315, 2024-03-15, 2024/03/15, 2024.03.15, 15/03/2024, 03/15/2024,
15.03.2024 and 15-03-2024. Letters in any case. A needle that is a number
is matched only where no digit is next to it, so it is not found inside a
longer number (a remapped UID is a long run of digits); a date may be
followed by digits, as in a date and time.

Two searches of each result:

  - every element's value as pydicom decodes it, the file meta information,
    private elements and every item of every sequence included, Pixel Data
    excepted;
  - the raw bytes of the whole file, Pixel Data's value cut out, for each
    form encoded in UTF-8 and in the original's character set (its
    Specific Character Set, as Python's codecs write it), for what a parser
    could miss.

Values shorter than 4 characters are not searched for (they match inside
ordinary text), and nor is one character of a script written without
spaces (Han, kana, Hangul), whose two or more characters are searched
for; UIDs the standard registers are not needles. What is skipped is
counted and printed, never passed over silently. The result must also say
Patient Identity Removed (0012,0062) YES.

Exit status 0 when no needle is found in any result and at least one was
searched for; 1 otherwise. A finding prints the original, the form and
where it was found (the values are synthetic or public test data).
"""
import json
import re
import sys
import unicodedata

import pydicom
from pydicom.charset import convert_encodings
from pydicom.uid import UID

MIN_LENGTH = 4
PIXEL_TAGS = {0x7FE00008, 0x7FE00009, 0x7FE00010}

# The attributes PS3.15 Table E.1-1 removes or replaces in the basic
# profile and no option keeps, searched for whatever a profile decided.
FLOOR = {
    "PatientName", "PatientID", "OtherPatientIDs", "OtherPatientNames",
    "OtherPatientIDsSequence", "PatientBirthName", "PatientBirthDate",
    "PatientBirthTime", "PatientAddress", "PatientMotherBirthName",
    "PatientTelephoneNumbers", "PatientTelecomInformation", "PersonAddress",
    "PersonTelephoneNumbers", "PersonTelecomInformation",
    "ReferringPhysicianAddress", "ReferringPhysicianTelephoneNumbers",
}


def unspaced(s):
    """Whether s holds a letter of a script written without spaces."""
    for ch in s:
        name = unicodedata.name(ch, "")
        if name.startswith(("CJK", "HIRAGANA", "KATAKANA", "HANGUL")):
            return True
    return False


class Needles:
    """The forms searched for, and what was skipped."""

    def __init__(self):
        self.forms = {}  # lowered form -> (needle, kind)
        self.skipped = 0

    def add_form(self, form, needle, kind):
        form = form.strip(" \x00")
        if not form:
            return
        if unspaced(form):
            if len(form) < 2:
                self.skipped += 1
                return
        elif len(form) < MIN_LENGTH:
            self.skipped += 1
            return
        self.forms.setdefault(form.lower(), (needle, kind))

    def add(self, kind, value):
        value = value.strip(" \x00")
        if not value:
            return
        if kind == "uid":
            try:
                if UID(value).is_valid and UID(value).name != value:
                    return
            except Exception:  # not a UID pydicom knows how to name
                pass
        self.add_form(value, value, kind)
        if kind == "name":
            for group in value.split("="):
                parts = [p.strip() for p in group.split("^") if p.strip()]
                for p in parts:
                    self.add_form(p, value, kind)
                    for w in p.split():
                        self.add_form(w, value, kind)
                if len(parts) >= 2:
                    fam, given = parts[0], parts[1]
                    for f in (f"{given} {fam}", f"{fam} {given}", f"{fam}, {given}",
                              f"{fam},{given}", " ".join(parts)):
                        self.add_form(f, value, kind)
        if kind == "date" and re.match(r"^\d{8}", value):
            y, m, d = value[:4], value[4:6], value[6:8]
            for f in (value[:8], f"{y}-{m}-{d}", f"{y}/{m}/{d}", f"{y}.{m}.{d}",
                      f"{d}/{m}/{y}", f"{m}/{d}/{y}", f"{d}.{m}.{y}", f"{d}-{m}-{y}"):
                self.add_form(f, value, kind)


def values_of(elem):
    """The text forms of an element's value."""
    v = elem.value
    items = list(v) if isinstance(v, (list, pydicom.multival.MultiValue)) else [v]
    out = []
    for x in items:
        if x is None:
            continue
        if isinstance(x, bytes):
            out.append(x.decode("latin-1"))
        else:
            out.append(str(x))
    if len(items) > 1:
        out.append("\\".join(str(x) for x in items))
    return out


def kind_of(elem):
    return {"PN": "name", "DA": "date", "DT": "date", "UI": "uid"}.get(elem.VR, "text")


def gather(orig, keywords, needles):
    """Needles pydicom reads from the original."""
    standard_text = []
    private = []
    for elem in orig.iterall():
        if elem.VR == "SQ" or elem.tag in PIXEL_TAGS:
            continue
        try:
            vals = values_of(elem)
        except Exception:  # a value pydicom cannot convert
            continue
        if elem.tag.is_private:
            if not elem.tag.is_private_creator and elem.VR in ("LO", "LT", "SH", "ST", "UT", "UC", "PN", "UN", "CS", "DA", "DT"):
                private.extend(vals)
            continue
        standard_text.extend(vals)
        if elem.keyword in keywords or elem.keyword in FLOOR or elem.VR == "PN":
            for s in vals:
                needles.add(kind_of(elem), s)
    hay = "\x00".join(standard_text).lower()
    for s in private:
        s = s.strip(" \x00")
        if not s.isprintable() or s.lower() in hay:
            continue
        needles.add("text", s)


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
                out.append((str(elem.tag), s.lower()))
        except Exception:  # a value pydicom cannot convert is read as bytes
            out.append((str(elem.tag), repr(getattr(elem, "value", b"")).lower()))
    return out


def pattern(form, kind):
    """A regular expression for a form (text or bytes): a form that begins
    with a digit must not follow one, and one that ends with a digit must
    not be followed by one, but for a date, which a time may follow."""
    as_bytes = isinstance(form, bytes)
    text = form.decode("latin-1") if as_bytes else form
    body = re.escape(form)
    if text[:1].isdigit():
        body = (rb"(?<![0-9])" if as_bytes else r"(?<![0-9])") + body
    if text[-1:].isdigit() and kind != "date":
        body = body + (rb"(?![0-9])" if as_bytes else r"(?![0-9])")
    return re.compile(body)


def raw_without_pixels(path, ds):
    with open(path, "rb") as f:
        raw = f.read()
    for t in PIXEL_TAGS:
        if t in ds:
            v = ds[t].value
            if isinstance(v, bytes) and v:
                i = raw.rfind(v)
                if i >= 0:
                    raw = raw[:i] + raw[i + len(v):]
    return raw.lower()


def codecs_of(orig):
    try:
        cs = orig.get("SpecificCharacterSet", "ISO_IR 6")
        cs = list(cs) if isinstance(cs, pydicom.multival.MultiValue) else [cs]
        return [c for c in set(convert_encodings(cs)) if c not in ("utf-8", "ascii")]
    except Exception:
        return ["latin-1"]


def encoded(form, codec):
    """form in codec, its escape sequences removed (ISO 2022)."""
    try:
        b = form.encode(codec)
    except Exception:
        return None
    b = re.sub(rb"\x1b[\x24\x28\x29\x2d][\x40-\x7e]?[\x40-\x7e]", b"", b)
    return b if len(b) >= MIN_LENGTH else None


def main():
    with open(sys.argv[1], encoding="utf-8") as f:
        spec = json.load(f)
    keywords = set(spec["keywords"])
    searched = skipped = bad = 0
    for pair in spec["files"]:
        orig = pydicom.dcmread(pair["original"], force=True)
        out = pydicom.dcmread(pair["deidentified"])
        if str(out.get("PatientIdentityRemoved", "")) != "YES":
            print(f"{pair['deidentified']}: Patient Identity Removed is not YES")
            bad += 1
        needles = Needles()
        with open(pair["needles"], encoding="utf-8") as f:
            for n in json.load(f):
                needles.add(n["kind"], n["value"])
        gather(orig, keywords, needles)
        searched += len(needles.forms)
        skipped += needles.skipped
        hay = texts(out)
        raw = raw_without_pixels(pair["deidentified"], out)
        codecs = codecs_of(orig)
        for form, (needle, kind) in sorted(needles.forms.items()):
            rx = pattern(form, kind)
            hits = [where for where, s in hay if rx.search(s)]
            if not hits:
                raws = set()
                for variant in (form, form.upper()):
                    raws.add(variant.encode("utf-8").lower())
                    raws.update(e.lower() for e in (encoded(variant, c) for c in codecs) if e)
                hits = ["raw bytes" for b in sorted(raws) if pattern(b, kind).search(raw)]
            if hits:
                print(f"{pair['original']}: {needle!r} found as {form!r} in {hits[0]} of the de-identified file")
                bad += 1
    print(f"{len(spec['files'])} files, {searched} forms searched for, {skipped} skipped as too short, {bad} found")
    if bad or searched == 0:
        sys.exit(1)


if __name__ == "__main__":
    main()
