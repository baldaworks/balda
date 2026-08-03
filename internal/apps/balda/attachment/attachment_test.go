package attachment

import "testing"

func TestNormalizeVoiceDescriptor(t *testing.T) {
	got, ok := Normalize(Descriptor{
		Kind:         " VOICE ",
		FileID:       " voice-file-id ",
		FileUniqueID: " voice-unique-id ",
		MIMEType:     " audio/ogg ",
		SizeBytes:    -1,
		Caption:      " caption ",
	})
	if !ok {
		t.Fatal("Normalize() ok = false, want true")
	}
	if got.Kind != KindVoice {
		t.Fatalf("kind = %q, want %q", got.Kind, KindVoice)
	}
	if got.FileID != "voice-file-id" || got.FileUniqueID != "voice-unique-id" {
		t.Fatalf("file identifiers = %+v", got)
	}
	if got.MIMEType != "audio/ogg" || got.Caption != "caption" {
		t.Fatalf("metadata = %+v", got)
	}
	if got.SizeBytes != 0 {
		t.Fatalf("size = %d, want 0 after negative normalization", got.SizeBytes)
	}
}

func TestNormalizeVoiceDescriptorRequiresSource(t *testing.T) {
	if _, ok := Normalize(Descriptor{Kind: KindVoice}); ok {
		t.Fatal("Normalize() ok = true, want false without file ID or blob")
	}
}
