package resourcedb

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func TestImportDatabaseIDs(t *testing.T) {
	r := ResourceDb()
	if r.Importer == nil {
		t.Fatal("database resource must support import")
	}
	for _, tc := range []struct{ id, name, cluster string }{
		{"analytics", "analytics", ""}, {":analytics", "analytics", ""}, {"test_cluster:analytics", "analytics", "test_cluster"}, {"test_cluster:events:archive", "events:archive", "test_cluster"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			d := schema.TestResourceDataRaw(t, r.Schema, nil)
			d.SetId(tc.id)
			result, err := r.Importer.StateContext(context.Background(), d, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(result) != 1 || d.Get("name") != tc.name || d.Get("cluster") != tc.cluster {
				t.Fatalf("unexpected import state: %v", d.State())
			}
		})
	}
	for _, id := range []string{"", ":", "cluster:"} {
		d := schema.TestResourceDataRaw(t, r.Schema, nil)
		d.SetId(id)
		if result, err := r.Importer.StateContext(context.Background(), d, nil); err == nil || result != nil {
			t.Fatalf("accepted invalid ID %q", id)
		}
	}
}
