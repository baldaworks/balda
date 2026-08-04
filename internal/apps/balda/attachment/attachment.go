package attachment

import (
	"strings"
)

type Kind string

const (
	KindPhoto    Kind = "photo"
	KindDocument Kind = "document"
	KindVoice    Kind = "voice"
)

// Descriptor is transport-neutral attachment metadata carried through turns.
// It intentionally supports future blob-backed storage without requiring it in V1.
type Descriptor struct {
	Kind         Kind     `json:"kind"`
	FileID       string   `json:"file_id,omitempty"`
	FileUniqueID string   `json:"file_unique_id,omitempty"`
	FileName     string   `json:"file_name,omitempty"`
	MIMEType     string   `json:"mime_type,omitempty"`
	SizeBytes    int64    `json:"size_bytes,omitempty"`
	Caption      string   `json:"caption,omitempty"`
	Blob         *BlobRef `json:"blob,omitempty"`
}

type BlobRef struct {
	Store  string `json:"store,omitempty"`
	Key    string `json:"key,omitempty"`
	Path   string `json:"path,omitempty"`
	URL    string `json:"url,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

func NormalizeList(in []Descriptor) []Descriptor {
	if len(in) == 0 {
		return nil
	}
	out := make([]Descriptor, 0, len(in))
	for _, item := range in {
		if normalized, ok := Normalize(item); ok {
			out = append(out, normalized)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func Normalize(in Descriptor) (Descriptor, bool) {
	in.Kind = Kind(strings.ToLower(strings.TrimSpace(string(in.Kind))))
	in.FileID = strings.TrimSpace(in.FileID)
	in.FileUniqueID = strings.TrimSpace(in.FileUniqueID)
	in.FileName = strings.TrimSpace(in.FileName)
	in.MIMEType = strings.TrimSpace(in.MIMEType)
	in.Caption = strings.TrimSpace(in.Caption)
	if in.Blob != nil {
		blob := *in.Blob
		blob.Store = strings.TrimSpace(blob.Store)
		blob.Key = strings.TrimSpace(blob.Key)
		blob.Path = strings.TrimSpace(blob.Path)
		blob.URL = strings.TrimSpace(blob.URL)
		blob.SHA256 = strings.TrimSpace(blob.SHA256)
		if blob.Store == "" && blob.Key == "" && blob.Path == "" && blob.URL == "" && blob.SHA256 == "" {
			in.Blob = nil
		} else {
			in.Blob = &blob
		}
	}
	switch in.Kind {
	case KindPhoto, KindDocument, KindVoice:
	default:
		return Descriptor{}, false
	}
	if in.FileID == "" && in.Blob == nil {
		return Descriptor{}, false
	}
	if in.SizeBytes < 0 {
		in.SizeBytes = 0
	}
	return in, true
}
