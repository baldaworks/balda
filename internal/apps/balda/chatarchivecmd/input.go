package chatarchivecmd

import (
	"path"
	"slices"
	"strings"
)

// Clone isolates a snapshot's attachment labels from later request enrichment.
func (i *Input) Clone() *Input {
	if i == nil {
		return nil
	}
	out := *i
	out.Attachments = slices.Clone(i.Attachments)
	return &out
}

// AttachmentLabel retains a filename and kind without paths or download URLs.
func AttachmentLabel(name, kind string) Attachment {
	name = strings.TrimSpace(name)
	if strings.Contains(name, "://") {
		name = ""
	}
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == "/" {
		name = ""
	}
	return Attachment{Name: name, Kind: strings.TrimSpace(kind)}
}
