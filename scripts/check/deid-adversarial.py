"""Write synthetic DICOM files that hide identifying values where they are
known to slip past a de-identifier, for the de-identification check.

Usage: deid-adversarial.py <directory>

Every value is made up. The files, written with pydicom:

  cjk-iso2022.dcm   a Japanese name in ISO 2022 IR 87 (ideographic and
                    phonetic groups of two-character parts), repeated in
                    free text with and without a space
  cjk-utf8.dcm      the same in ISO_IR 192, and a Korean name of a
                    one-syllable family name
  latin1.dcm        ISO_IR 100 with umlauts and an eszett, the name in free
                    text as "Given Family", the birth date written
                    2024-style in a comment
  short-names.dcm   a name of two-letter parts, in free text
  un-private.dcm    a private element written as UN with a defined length
                    whose bytes are an item holding a name, the form a
                    sequence takes through a system without the private
                    dictionary (PS3.5 6.2.2)
  crafted-utf8.dcm  ISO_IR 192 text with letters whose lower case is longer
                    in bytes (İ), and an identifier cut in two by another,
                    which joins again once that one is removed

Each holds a Patient ID, so a profile that shifts dates or hashes it can
work, and SOP Class and Instance UIDs of a CT image.
"""
import os
import struct
import sys

import pydicom
from pydicom.dataset import Dataset, FileMetaDataset
from pydicom.dataelem import DataElement
from pydicom.uid import ExplicitVRLittleEndian

CT = "1.2.840.10008.5.1.4.1.1.2"


def base(n, charset=None):
    ds = Dataset()
    if charset:
        ds.SpecificCharacterSet = charset
    ds.SOPClassUID = CT
    ds.SOPInstanceUID = f"1.2.826.0.1.3680043.10.999.{n}.1"
    ds.StudyInstanceUID = f"1.2.826.0.1.3680043.10.999.{n}.2"
    ds.SeriesInstanceUID = f"1.2.826.0.1.3680043.10.999.{n}.3"
    ds.Modality = "CT"
    ds.StudyDate = "20240315"
    return ds


def save(ds, path):
    meta = FileMetaDataset()
    meta.MediaStorageSOPClassUID = ds.SOPClassUID
    meta.MediaStorageSOPInstanceUID = ds.SOPInstanceUID
    meta.TransferSyntaxUID = ExplicitVRLittleEndian
    ds.file_meta = meta
    # pydicom 3 asks for the file format by name; pydicom 2 by the encoding.
    if "enforce_file_format" in ds.save_as.__code__.co_varnames:
        ds.save_as(path, enforce_file_format=True)
    else:
        ds.is_little_endian, ds.is_implicit_VR = True, False
        ds.save_as(path, write_like_original=False)


def main():
    out = sys.argv[1]
    os.makedirs(out, exist_ok=True)

    ds = base(1, ["", "ISO 2022 IR 87"])
    ds.PatientName = "Yamada^Tarou=山田^太郎=やまだ^たろう"
    ds.PatientID = "JP-778812"
    ds.StudyDescription = "山田 太郎 頭部"
    ds.PatientComments = "山田太郎 さん"
    save(ds, os.path.join(out, "cjk-iso2022.dcm"))

    ds = base(2, "ISO_IR 192")
    ds.PatientName = "山田^太郎"
    ds.PatientID = "JP-778813"
    ds.ReferringPhysicianName = "김^민수"
    ds.StudyDescription = "山田太郎さんのCT 김 민수"
    ds.ImageComments = "やまだ 山田"
    save(ds, os.path.join(out, "cjk-utf8.dcm"))

    ds = base(3, "ISO_IR 100")
    ds.PatientName = "Müller^Jürgen"
    ds.PatientID = "DE-445566"
    ds.PatientBirthDate = "19611213"
    ds.PatientAddress = "Hauptstraße 5, Köln"
    ds.StudyDescription = "Kopf CT Jürgen Müller"
    ds.PatientComments = "geboren 1961-12-13, Hauptstraße 5, Köln"
    save(ds, os.path.join(out, "latin1.dcm"))

    ds = base(4)
    ds.PatientName = "Li^Al"
    ds.PatientID = "SN-1002"
    ds.StudyDescription = "Li Al follow-up, Alpha protocol"
    save(ds, os.path.join(out, "short-names.dcm"))

    ds = base(5)
    ds.PatientName = "Hidden^Person"
    ds.PatientID = "UN-2203"
    ds.add_new(0x00290010, "LO", "ADVERSARY 1.0")
    name = b"Hidden^Person "
    inner = struct.pack("<HHI", 0x0010, 0x0010, len(name)) + name
    item = struct.pack("<HHI", 0xFFFE, 0xE000, len(inner)) + inner
    ds.add(DataElement(0x00291010, "UN", item))
    save(ds, os.path.join(out, "un-private.dcm"))

    ds = base(6, "ISO_IR 192")
    ds.PatientName = "İnce^Ömer"
    ds.PatientID = "CR-9001"
    ds.StudyDescription = "İNCE ÖMER, Ömer İnce, id CR-9CR-9001001"
    ds.ImageComments = "Ömer"
    save(ds, os.path.join(out, "crafted-utf8.dcm"))


if __name__ == "__main__":
    main()
