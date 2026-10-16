package resourcetable

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func TestImportTableIDs(t *testing.T) {
	for _, tc := range []struct{ id, cluster, database, name, wantID string }{
		{"db:table", "", "db", "table", ":db:table"},
		{"cluster:db:table", "cluster", "db", "table", "cluster:db:table"},
		{":db:table", "", "db", "table", ":db:table"},
		{"data-base:table name", "", "data-base", "table name", ":data-base:table name"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			d := schema.TestResourceDataRaw(t, ResourceTable().Schema, nil)
			d.SetId(tc.id)
			got, err := importTable(context.Background(), d, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0] != d || d.Id() != tc.wantID || d.Get("cluster") != tc.cluster || d.Get("database") != tc.database || d.Get("name") != tc.name {
				t.Fatalf("imported state = %#v, ID = %q", d.State(), d.Id())
			}
		})
	}
	for _, id := range []string{"", "table", "db:", ":table", "cluster::table", "cluster:db:", "a:b:c:d"} {
		t.Run("invalid_"+id, func(t *testing.T) {
			d := schema.TestResourceDataRaw(t, ResourceTable().Schema, nil)
			d.SetId(id)
			if result, err := importTable(context.Background(), d, nil); err == nil || result != nil {
				t.Fatalf("accepted ID %q", id)
			}
		})
	}
}

func TestImportTableCommentCompatibility(t *testing.T) {
	for _, tc := range []struct{ stored, want, cluster string }{
		{"owner's externally created table", "owner's externally created table", ""},
		{`{"owner":"external","retention":30}`, `{"owner":"external","retention":30}`, ""},
		{`{"comment":"plain JSON without cluster"}`, `{"comment":"plain JSON without cluster"}`, ""},
		{`{"comment":null,"cluster":"other"}`, `{"comment":null,"cluster":"other"}`, ""},
		{`{"comment":5,"cluster":"other"}`, `{"comment":5,"cluster":"other"}`, ""},
		{`{"comment":"owner's \"quoted\" table","cluster":"local"}`, `owner's "quoted" table`, "local"},
		{`{"comment":"owner\'s table","cluster":"local"}`, "owner's table", "local"},
	} {
		t.Run(tc.stored, func(t *testing.T) {
			table := CHTable{Name: "t", Database: "db", Engine: "MergeTree", EngineFull: "MergeTree", Comment: tc.stored}
			got, err := table.ToResource()
			if err != nil {
				t.Fatal(err)
			}
			if got.Comment != tc.want || got.Cluster != tc.cluster {
				t.Fatalf("comment = %q, cluster = %q; want %q, %q", got.Comment, got.Cluster, tc.want, tc.cluster)
			}
		})
	}
}
