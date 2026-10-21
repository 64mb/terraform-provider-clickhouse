# Local topology fixture

`clickhouse-topology.xml` adds one embedded Keeper, one local shard/replica named `local_provider`, and the `shard=01` / `replica=replica1` macros to ClickHouse 25.3. Keeper and the remote replica remain inside the container; only the native client port is published to localhost.

Run from the repository root:

```sh
docker run --rm -d --name clickhouse-provider-topology \
  -p 127.0.0.1:19001:9000 \
  -e CLICKHOUSE_SKIP_USER_SETUP=1 \
  -e CLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1 \
  -v "$PWD/tests/fixtures/clickhouse-topology.xml:/etc/clickhouse-server/config.d/provider-topology.xml:ro" \
  clickhouse/clickhouse-server:25.3

until docker exec clickhouse-provider-topology clickhouse-client \
  --query "SELECT count() FROM system.clusters WHERE cluster = 'local_provider'"; do
  sleep 1
done

CLICKHOUSE_TOPOLOGY_TEST_ADDR=127.0.0.1:19001 \
  go test -v -count=1 -timeout 4m ./pkg/resources/table \
  -run '^TestIntegrationTopologyImports$'

docker stop clickhouse-provider-topology
```

The test uses provider CREATE with `ON CLUSTER`, then imports through an explicit `local_provider:default:table` ID. It checks exact engine arguments, preserves that cluster override, verifies unchanged plans, and adds a column to replicated tables while preserving UUIDs and rows. Distributed reads route through the real local shard; mutation attempts require an external topology migration.

Each run uses unique table names and removes its own tables. The separate port leaves a basic ClickHouse fixture on port 19000 untouched. This is a single-node functional test, not a multi-replica failover or durability test. Configuration reference: [ClickHouse Keeper](https://clickhouse.com/docs/guides/sre/keeper/clickhouse-keeper).

## Two-node ON CLUSTER fixture

`cluster/compose.yml` runs two shards with one replica each and a separate
`clickhouse/clickhouse-keeper` service. All three containers use `latest`. Native
ports bind only to localhost, on 19002 and 19003. The fixture is independent of
the root development Compose file and the single-node fixture above.

```sh
docker compose -f tests/fixtures/cluster/compose.yml pull
docker compose -f tests/fixtures/cluster/compose.yml up -d --wait --wait-timeout 120

TF_ACC=1 CLICKHOUSE_CLUSTER_TEST_ADDRS=127.0.0.1:19002,127.0.0.1:19003 \
  go test -v -count=1 -timeout 8m ./pkg/provider ./pkg/resources/db ./pkg/resources/table \
  -run '^(TestAccCluster|TestIntegrationCluster)'

docker compose -f tests/fixtures/cluster/compose.yml stop node2
CLICKHOUSE_CLUSTER_OFFLINE_TEST_ADDR=127.0.0.1:19002 \
  go test -v -count=1 -timeout 1m ./pkg/resources/db ./pkg/resources/table \
  -run '^TestIntegrationCluster(Database)?Unavailable$'

docker compose -f tests/fixtures/cluster/compose.yml down -v
```

Terraform must be available on `PATH`. The tests exercise inherited and explicit
cluster targets, the documented `'{cluster}'` macro, database creation/deletion,
MergeTree and ReplicatedMergeTree ALTER, Distributed reads, import, unchanged
plans, and destruction. Assertions query both nodes directly and check data and
UUIDs. Failure tests cover unknown clusters, CREATE/ALTER/DROP errors on only the
second node, hidden errors under a `never_throw` profile, an offline node, and
refusal to delete a database containing a table on only the second node.

The GitHub Actions `cluster-integration` job runs this fixture once, without a
version or CLI matrix. The existing standalone version matrix remains separate.
Tests use unique object names. The offline case leaves a queued CREATE that may
execute after the node restarts, so remove the fixture with `down -v` after testing.
This is a distributed DDL test, not a replicated-data failover or durability test.
