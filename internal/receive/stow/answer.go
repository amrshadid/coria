package stow

import (
	"encoding/json"

	"github.com/amrshadid/go-dicom/dataelem"
	"github.com/amrshadid/go-dicom/dataset"
	"github.com/amrshadid/go-dicom/sequence"
	"github.com/amrshadid/go-dicom/tag"
)

// The attributes of the answer (PS3.18 10.5.3.2, the Store Instances
// Response Module of PS3.18 Annex I).
var (
	tagRetrieveURL    = tag.New(0x0008, 0x1190)
	tagFailureReason  = tag.New(0x0008, 0x1197)
	tagFailedSOPs     = tag.New(0x0008, 0x1198)
	tagReferencedSOPs = tag.New(0x0008, 0x1199)
	tagRefSOPClass    = tag.New(0x0008, 0x1150)
	tagRefSOPInstance = tag.New(0x0008, 0x1155)
)

// answer returns the DICOM JSON (PS3.18 F.2) of a request's answer, written
// by go-dicom: the Referenced SOP Sequence (0008,1199), each stored
// instance with its Retrieve URL, the WADO-RS URL of the instance (PS3.18
// 10.4.1); the Failed SOP Sequence (0008,1198), each refused part that has
// UIDs to list with its Failure Reason; and the study's Retrieve URL when
// what was stored is of one study, or of the study the request named.
func answer(base, study string, outcomes []outcome) ([]byte, error) {
	stored, failed := sequence.New(), sequence.New()
	studies := map[string]bool{}
	for _, o := range outcomes {
		if !o.listed {
			continue
		}
		item := dataset.NewDataset()
		if err := add(item, tagRefSOPClass, dataelem.UI, o.class); err != nil {
			return nil, err
		}
		if err := add(item, tagRefSOPInstance, dataelem.UI, o.instance); err != nil {
			return nil, err
		}
		if o.reason != 0 {
			if err := add(item, tagFailureReason, dataelem.US, o.reason); err != nil {
				return nil, err
			}
			if err := failed.Append(item); err != nil {
				return nil, err
			}
			continue
		}
		studies[o.study] = true
		if err := add(item, tagRetrieveURL, dataelem.UR, base+"/studies/"+o.study+"/series/"+o.series+"/instances/"+o.instance); err != nil {
			return nil, err
		}
		if err := stored.Append(item); err != nil {
			return nil, err
		}
	}
	ds := dataset.NewDataset()
	if study == "" && len(studies) == 1 {
		for only := range studies {
			study = only
		}
	}
	if study != "" && stored.Length() > 0 {
		if err := add(ds, tagRetrieveURL, dataelem.UR, base+"/studies/"+study); err != nil {
			return nil, err
		}
	}
	if stored.Length() > 0 {
		if err := add(ds, tagReferencedSOPs, dataelem.SQ, stored); err != nil {
			return nil, err
		}
	}
	if failed.Length() > 0 {
		if err := add(ds, tagFailedSOPs, dataelem.SQ, failed); err != nil {
			return nil, err
		}
	}
	m, err := ds.ToDICOMJSON()
	if err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// add adds one element to a data set.
func add(ds *dataset.Dataset, t tag.Tag, vr dataelem.VR, value any) error {
	return ds.Add(dataelem.NewDataElement(t, vr, value))
}
