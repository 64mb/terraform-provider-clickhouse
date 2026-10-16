package resourcetable

import (
	"reflect"
	"strings"
	"testing"
)

func TestColumnTTLs(t *testing.T) {
	create := "CREATE TABLE `db`.`table` (\n `at` DateTime COMMENT 'column TTL ignored' CODEC(Delta(4), ZSTD(1)) TTL at + INTERVAL 7 DAY,\n `a,b` String DEFAULT 'TTL fake, CODEC(ZSTD)' COMMENT 'TTL no',\n `quoted``name` DateTime64(3) COMMENT 'CODEC(x)'\nTTL if(flag, at + INTERVAL 1 DAY, at + INTERVAL 2 DAY),\n INDEX idx at TYPE minmax GRANULARITY 1\n) ENGINE = MergeTree ORDER BY at TTL at + INTERVAL 30 DAY"
	want := map[string]string{"at": "at + INTERVAL 7 DAY", "quoted`name": "if(flag, at + INTERVAL 1 DAY, at + INTERVAL 2 DAY)"}
	if got := columnTTLs(create); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for _, s := range []string{"", "CREATE TABLE t (x DateTime TTL now()", "CREATE TABLE t (x String COMMENT 'TTL fake') ENGINE=Memory"} {
		if got := columnTTLs(s); len(got) != 0 {
			t.Fatalf("unexpected TTL from %q: %#v", s, got)
		}
	}
}

func TestColumnMetadataReadAndBuild(t *testing.T) {
	table := CHTable{CreateTableQuery: "CREATE TABLE t (`at` DateTime TTL at + INTERVAL 1 DAY) ENGINE=MergeTree ORDER BY at", Columns: []CHColumn{{Name: "at", Type: "DateTime", Codec: "CODEC(Delta(4), ZSTD(1))", Comment: "with 'quote'"}}}
	cols := table.ColumnsToResource()
	got := cols[0].(map[string]interface{})
	if got["codec"] != "Delta(4), ZSTD(1)" || got["ttl"] != "at + INTERVAL 1 DAY" {
		t.Fatalf("lost column metadata: %#v", got)
	}
	r := TableResource{Columns: cols}
	column := r.GetColumnsResourceList()[0]
	sql := buildColumnsSentence([]ColumnResource{column})[0]
	if !(strings.Index(sql, " COMMENT ") < strings.Index(sql, " CODEC(") && strings.Index(sql, " CODEC(") < strings.Index(sql, " TTL ")) {
		t.Fatalf("invalid column clause order: %s", sql)
	}
	for _, want := range []string{"`at` DateTime", " TTL at + INTERVAL 1 DAY", " CODEC(Delta(4), ZSTD(1))", " COMMENT 'with \\'quote\\''"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("missing %q in %s", want, sql)
		}
	}
}

func TestSamplingKeyReadAndBuild(t *testing.T) {
	for _, key := range []string{"", "cityHash64(id)"} {
		table := CHTable{Name: "t", Database: "db", Engine: "MergeTree", EngineFull: "MergeTree", SamplingKey: key, SortingKey: "cityHash64(id)", PrimaryKey: "cityHash64(id)", CreateTableQuery: "CREATE TABLE db.t (id UInt64) ENGINE=MergeTree ORDER BY cityHash64(id) SAMPLE BY cityHash64(id) TTL at + INTERVAL 1 DAY"}
		resource, err := table.ToResource()
		if err != nil {
			t.Fatal(err)
		}
		if resource.SampleBy != "cityHash64(id)" {
			t.Fatalf("lost sampling key: %#v", resource)
		}
		sql := buildCreateOnClusterSentence(*resource)
		if !strings.Contains(sql, " PRIMARY KEY (cityHash64(id)) SAMPLE BY cityHash64(id) TTL ") {
			t.Fatalf("invalid sampling clause: %s", sql)
		}
	}
}

func TestMetadataKeywordIdentifiers(t *testing.T) {
	for _, column := range []string{"ttl", "comment", "settings"} {
		query := "CREATE TABLE default.ttl (`ttl` DateTime, `comment` DateTime, `settings` DateTime) ENGINE = MergeTree ORDER BY ttl TTL " + column + " + toIntervalDay(1) SETTINGS index_granularity = 8192 COMMENT 'table'"
		clauses := sqlClauses(query)
		if clauses["ORDER BY"] != "ttl" || clauses["TTL"] != column+" + toIntervalDay(1)" || clauses["SETTINGS"] != "index_granularity = 8192" || clauses["COMMENT"] != "'table'" {
			t.Fatalf("incorrect table clauses: %#v", clauses)
		}
		query = "CREATE TABLE default.ttl (`expire` DateTime DEFAULT ttl + toIntervalDay(2) COMMENT 'TTL fake' CODEC(ZSTD(1)) TTL " + column + " + toIntervalDay(1), `ttl` DateTime) ENGINE=MergeTree ORDER BY ttl"
		if got := columnTTLs(query); got["expire"] != column+" + toIntervalDay(1)" || len(got) != 1 {
			t.Fatalf("incorrect column TTL: %#v", got)
		}
	}
	query := "CREATE TABLE default.ttl (`expire` DateTime DEFAULT ttl + toIntervalDay(2), `ttl` DateTime) ENGINE = MergeTree ORDER BY ttl"
	if clauses := sqlClauses(query); clauses["TTL"] != "" || clauses["ORDER BY"] != "ttl" {
		t.Fatalf("invented table TTL: %#v", clauses)
	}
	if got := columnTTLs(query); len(got) != 0 {
		t.Fatalf("invented column TTL: %#v", got)
	}
}
