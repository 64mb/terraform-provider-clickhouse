package common

import "testing"

func TestUnmarshalCommentExternalJSON(t *testing.T) {
	for _, stored := range []string{"", "plain comment", `{}`, `null`, `[]`, `{"comment":null,"cluster":"prod"}`, `{"comment":"text","cluster":null}`, `{"comment":7,"cluster":false}`, `{"owner":"analytics"}`, `{"comment":"business meaning"}`} {
		t.Run(stored, func(t *testing.T) {
			comment, cluster, err := UnmarshalComment(stored)
			if err == nil {
				t.Fatalf("external comment treated as provider metadata: comment=%q cluster=%q", comment, cluster)
			}
		})
	}
}

func TestUnmarshalCommentMetadata(t *testing.T) {
	comment, cluster, err := UnmarshalComment(`{"comment":"business's \\\\ archive","cluster":"prod"}`)
	if err != nil || comment != "business's \\\\ archive" || cluster != "prod" {
		t.Fatalf("metadata: %q %q %v", comment, cluster, err)
	}
}

func TestUnmarshalCommentLegacyApostrophe(t *testing.T) {
	comment, cluster, err := UnmarshalComment(`{"comment":"owner\'s database","cluster":"test_cluster"}`)
	if err != nil || comment != "owner's database" || cluster != "test_cluster" {
		t.Fatalf("legacy metadata: comment=%q cluster=%q err=%v", comment, cluster, err)
	}
}

func TestUnmarshalCommentModernBackslashApostrophe(t *testing.T) {
	comment, cluster, err := UnmarshalComment(`{"comment":"owner\\'s database","cluster":"test_cluster"}`)
	if err != nil || comment != `owner\'s database` || cluster != "test_cluster" {
		t.Fatalf("modern metadata: comment=%q cluster=%q err=%v", comment, cluster, err)
	}
}

func TestUnmarshalCommentMetadataExtraFields(t *testing.T) {
	comment, cluster, err := UnmarshalComment(`{"comment":"business","cluster":"prod","owner":"external"}`)
	if err != nil || comment != "business" || cluster != "prod" {
		t.Fatalf("extended metadata: comment=%q cluster=%q err=%v", comment, cluster, err)
	}
}
