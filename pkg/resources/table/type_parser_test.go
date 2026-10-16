package resourcetable

import "testing"

func TestCanonicalType(t *testing.T) {
	cases := map[string]string{
		" UInt64 ":                                       "UInt64",
		"Array ( Nullable ( String ) )":                  "Array(Nullable(String))",
		"Map(String, Array(Decimal(38, 9)))":             "Map(String, Array(Decimal(38, 9)))",
		"Tuple(UInt8, String, DateTime64(3, 'UTC'))":     "Tuple(UInt8, String, DateTime64(3, 'UTC'))",
		"Tuple(id UInt64, `a,b` Array(String))":          "Tuple(`id` UInt64, `a,b` Array(String))",
		"Nested(id UUID, values Array(Nullable(Int64)))": "Nested(`id` UUID, `values` Array(Nullable(Int64)))",
		"Enum8('a,b' = -128, 'it\\'s (ok)'=127)":         "Enum8('a,b' = -128, 'it\\'s (ok)' = 127)",
		"Enum16('a''b' = -32768, 'other' = 32767)":       "Enum16('a\\'b' = -32768, 'other' = 32767)",
		"DateTime": "DateTime", "DateTime('Europe/Moscow')": "DateTime('Europe/Moscow')",
		"DateTime64": "DateTime64(3)", "DateTime64(9)": "DateTime64(9)", "Decimal32(9)": "Decimal(9, 9)", "Decimal64(18)": "Decimal(18, 18)", "Decimal128(38)": "Decimal(38, 38)", "Decimal256(76)": "Decimal(76, 76)", "FixedString(16)": "FixedString(16)", "Tuple()": "Tuple()",
		"LowCardinality(Nullable(String))": "LowCardinality(Nullable(String))", "Tuple(x Decimal32(2), y Array(Decimal64(3)))": "Tuple(`x` Decimal(9, 2), `y` Array(Decimal(18, 3)))", "IPv4": "IPv4", "IPv6": "IPv6", "JSON": "JSON",
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			got, err := CanonicalType(input)
			if err != nil || got != want {
				t.Fatalf("CanonicalType(%q) = %q, %v; want %q", input, got, err, want)
			}
			again, err := CanonicalType(got)
			if err != nil || again != got {
				t.Fatalf("canonical value does not round trip: %q, %v", again, err)
			}
		})
	}
}

func TestCanonicalTypeRejectsInvalidInput(t *testing.T) {
	cases := []string{"", "Unknown", "String()", "String; DROP TABLE x", "String -- comment", "String /* comment */", "String CODEC(ZSTD)", "Array", "Array()", "Array(String, UInt8)", "Array(String", "Nullable(String))", "Nullable(Array(String))", "Nullable(Nullable(String))", "Nullable(Tuple(String))", "Map(String)", "Map(String, UInt8, UInt8)", "Tuple(a UInt8, String)", "Nested(UInt8)", "Nested()", "Tuple(a UInt8, a String)", "Tuple(`` UInt8)", "Decimal(0, 0)", "Decimal(77, 0)", "Decimal(9, 10)", "Decimal(9)", "Decimal32(10)", "Decimal64(19)", "Decimal128(39)", "Decimal256(77)", "Decimal(-1, 0)", "FixedString(0)", "FixedString(999999999999999999999999999)", "DateTime64(10)", "DateTime64(-1)", "DateTime64(3, '')", "DateTime(UTC)", "DateTime('UTC', 'GMT')", "DateTime('UTC'", "Enum8()", "Enum8('a'=128)", "Enum8('a'=-129)", "Enum16('a'=32768)", "Enum8('a'=1, 'a'=2)", "Enum8('a'=1, 'b'=1)", "Enum8('a'=1,)", "Enum8('a'=1); DROP TABLE x", "Enum8('unterminated=1)", "JSON(max_dynamic_paths=1)", "String\x00"}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			if got, err := CanonicalType(input); err == nil {
				t.Fatalf("accepted invalid type %q as %q", input, got)
			}
		})
	}
}

func TestCanonicalTypeDepthLimit(t *testing.T) {
	s := "String"
	for i := 0; i < 100; i++ {
		s = "Array(" + s + ")"
	}
	if _, err := CanonicalType(s); err == nil {
		t.Fatal("accepted excessive nesting")
	}
}

func TestValidateDataType(t *testing.T) {
	if got := ValidateDataType("Array(UUID)", nil); len(got) != 0 {
		t.Fatalf("valid type rejected: %v", got)
	}
	if got := ValidateDataType("String; DROP TABLE t", nil); len(got) != 1 {
		t.Fatalf("expected invalid type diagnostic: %v", got)
	}
	if got := ValidateDataType(1, nil); len(got) != 1 {
		t.Fatalf("expected non-string diagnostic: %v", got)
	}
}

func TestAggregateFunctionTypes(t *testing.T) {
	cases := map[string]string{
		"AggregateFunction(custom(-0.0), UInt64)":                                "AggregateFunction(custom(0), UInt64)",
		"AggregateFunction(count)":                                               "AggregateFunction(count)",
		"AggregateFunction(sum, UInt64)":                                         "AggregateFunction(sum, UInt64)",
		"SimpleAggregateFunction(sum, UInt64)":                                   "SimpleAggregateFunction(sum, UInt64)",
		"SimpleAggregateFunction(sumMap, Array(String), Array(UInt64))":          "SimpleAggregateFunction(sumMap, Array(String), Array(UInt64))",
		"AggregateFunction(quantiles(0.50, 9e-1), Nullable(Decimal32(2)))":       "AggregateFunction(quantiles(0.5, 0.9), Nullable(Decimal(9, 2)))",
		"AggregateFunction(groupArray(0010), Tuple(String, UInt64))":             "AggregateFunction(groupArray(10), Tuple(String, UInt64))",
		"AggregateFunction(sequenceMatch('(?1).*(?2)'), DateTime, UInt8, UInt8)": "AggregateFunction(sequenceMatch('(?1).*(?2)'), DateTime, UInt8, UInt8)",
		"AggregateFunction(custom(+1, -2, .50, +3.0e+2, 'a,b\\'c'), UInt64)":     "AggregateFunction(custom(1, -2, 0.5, 300, 'a,b\\'c'), UInt64)",
		"AggregateFunction(custom(18446744073709551615), Nothing)":               "AggregateFunction(custom(18446744073709551615), Nothing)",
		"Nullable(Nothing)": "Nullable(Nothing)",
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			got, err := CanonicalType(input)
			if err != nil || got != want {
				t.Fatalf("got %q, %v; want %q", got, err, want)
			}
			again, err := CanonicalType(got)
			if err != nil || again != got {
				t.Fatalf("failed round trip: %q, %v", again, err)
			}
		})
	}
}

func TestAggregateFunctionRejectsInjection(t *testing.T) {
	cases := []string{
		"AggregateFunction()", "AggregateFunction(sum,)", "SimpleAggregateFunction(sum)",
		"AggregateFunction(sumState()) trailing", "AggregateFunction(sum; DROP TABLE t, UInt64)",
		"AggregateFunction(sum -- comment, UInt64)", "AggregateFunction(sum /* comment */, UInt64)",
		"AggregateFunction(sum(SELECT 1), UInt64)", "AggregateFunction(sum(foo()), UInt64)",
		"AggregateFunction(sum(1 + 2), UInt64)", "AggregateFunction(sum(1; DROP TABLE t), UInt64)",
		"AggregateFunction(sum(1e), UInt64)", "AggregateFunction(sum(1e99999), UInt64)",
		"AggregateFunction(sum(1,), UInt64)", "AggregateFunction(sum(,1), UInt64)",
		"AggregateFunction('sum', UInt64)", "AggregateFunction(db.sum, UInt64)",
		"AggregateFunction(sum, UInt64) CODEC(ZSTD)",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			if got, err := CanonicalType(input); err == nil {
				t.Fatalf("accepted %q as %q", input, got)
			}
		})
	}
}
