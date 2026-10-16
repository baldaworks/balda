package chatarchivecmd

import "testing"

func TestAttachmentLabelRetainsOnlyFilenameAndKind(t *testing.T) {
	for _, test := range []struct{ name, want string }{{"report.txt", "report.txt"}, {"/private/data/report.txt", "report.txt"}, {`C:\private\report.txt`, "report.txt"}, {"https://files.example/report.txt?token=secret", ""}, {"", ""}} {
		t.Run(test.name, func(t *testing.T) {
			got := AttachmentLabel(test.name, "document")
			if got.Name != test.want || got.Kind != "document" {
				t.Fatalf("label = %+v, want %q/document", got, test.want)
			}
		})
	}
}
