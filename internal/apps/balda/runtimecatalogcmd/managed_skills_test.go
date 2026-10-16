package runtimecatalogcmd

import "testing"

func TestManagedSkillSelectionExactEvidence(t *testing.T) {
	a := SourceDescriptor{ID: SourceID{Kind: SourceKindUserSkill, Name: "managed-a"}, Revision: "a1"}
	b := SourceDescriptor{ID: SourceID{Kind: SourceKindUserSkill, Name: "managed-b"}, Revision: "b1"}
	selection := ManagedSkillSelection{Generation: 4, Sources: []SourceDescriptor{b, a}}
	if !selection.Equal(ManagedSkillSelection{Generation: 4, Sources: []SourceDescriptor{a, b}}) {
		t.Fatal("descriptor order changed complete selection")
	}
	for _, different := range []ManagedSkillSelection{
		{Generation: 5, Sources: []SourceDescriptor{a, b}},
		{Generation: 4, Sources: []SourceDescriptor{a}},
		{Generation: 4},
		{Generation: 4, Sources: []SourceDescriptor{a, a}},
	} {
		if selection.Equal(different) {
			t.Fatalf("different publication evidence accepted: %+v", different)
		}
	}
	normalized := selection.Normalized()
	normalized.Sources[0].Revision = "edited"
	if selection.Sources[1].Revision != "a1" {
		t.Fatal("normalization mutated original evidence")
	}
	if !(ManagedSkillSelection{Generation: 4}).Equal(ManagedSkillSelection{Generation: 4, Sources: []SourceDescriptor{}}) {
		t.Fatal("empty selection was not explicit absence")
	}
	if (ManagedSkillSelection{Generation: 4}).Equal(ManagedSkillSelection{Generation: 5}) {
		t.Fatal("same empty catalog completed newer generation")
	}
}
