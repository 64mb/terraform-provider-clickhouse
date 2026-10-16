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
