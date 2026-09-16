import {
  Alert,
  Badge,
  Box,
  Card,
  HStack,
  Heading,
  SimpleGrid,
  Stack,
  Table,
  Text,
} from "@chakra-ui/react";
import { api } from "../api";
import { ErrorPanel, Loading, useLoad } from "../components/common";

/** What this node believes about itself.
 *
 * Read-only. Editing configuration from a browser would mean a running
 * node rewriting its own config.toml, and every "this section needs a
 * restart" caveat becomes a race somebody triggers by clicking Save.
 *
 * The node assembles this view from an allowlist rather than dumping
 * its config, so what is absent here is absent on purpose — endpoints
 * and credentials among it.
 */
export function Config() {
  const { data, error, loading } = useLoad(() => api.config());

  if (error) return <ErrorPanel error={error} />;
  if (loading && !data) return <Loading />;
  if (!data) return null;

  return (
    <Box>
      <Heading size="lg" mb={1}>
        Configuration
      </Heading>
      <Text color="fg.muted" fontSize="sm" mb={5}>
        {data.node_id}
        {data.version ? ` · ${data.version}` : ""} · {data.functions.join(", ")}
      </Text>

      {!data.gateway.require_auth && (
        <Alert.Root status="warning" mb={5}>
          <Alert.Indicator />
          <Alert.Content>
            <Alert.Title>This console is unauthenticated</Alert.Title>
            <Alert.Description>
              Anyone who can reach {data.gateway.bind_address || "this node"} can rewrite
              your bots. That is fine on loopback; set{" "}
              <code>[auth] require_auth</code> before binding anything else.
            </Alert.Description>
          </Alert.Content>
        </Alert.Root>
      )}

      <SimpleGrid columns={{ base: 1, md: 2 }} gap={4}>
        <Panel title="Gateway">
          <Row label="Bind" value={data.gateway.bind_address || "every interface"} />
          <Row label="Port" value={String(data.gateway.http_port)} />
          <Row label="Requires auth" value={yesNo(data.gateway.require_auth)} />
          <Row label="Console login" value={yesNo(data.gateway.login_configured)} />
          <Row label="Queue mode" value={data.gateway.queue_mode || "serial"} />
          <Row label="Timezone" value={data.gateway.default_timezone || "UTC"} />
        </Panel>

        <Panel title="Bots">
          <Row label="Queue depth limit" value={String(data.bots.max_pending)} />
          <Row label="Bots work their queues" value={yesNo(data.bots.drain_enabled)} />
        </Panel>

        <Panel title="Memory">
          <Row label="Enabled" value={yesNo(data.memory.enabled)} />
          <Row label="Dream schedule" value={data.memory.dream_schedule || "default"} />
          {/* The vector space the corpus is in — the one thing an
              operator debugging bad recall actually needs. */}
          <Row
            label="Embedding model"
            value={data.memory.embedding_model || "none (recall is lexical)"}
          />
        </Panel>

        <Panel title="Compute">
          <Row
            label="Tool calls per turn"
            value={String(data.compute.max_tool_calls_per_turn)}
          />
          <Row label="Self-learning" value={data.compute.self_learning_mode || "off"} />
        </Panel>
      </SimpleGrid>

      {data.compute.providers.length > 0 && (
        <Box mt={6}>
          <Heading size="md" mb={1}>
            Providers
          </Heading>
          <Text color="fg.muted" fontSize="sm" mb={3}>
            Labels and roles only — no endpoints, no model names, no credentials.
          </Text>
          <Table.Root size="sm" variant="line">
            <Table.Header>
              <Table.Row>
                <Table.ColumnHeader>Label</Table.ColumnHeader>
                <Table.ColumnHeader>Trust tier</Table.ColumnHeader>
                <Table.ColumnHeader>Roles</Table.ColumnHeader>
              </Table.Row>
            </Table.Header>
            <Table.Body>
              {data.compute.providers.map((p) => (
                <Table.Row key={p.label}>
                  <Table.Cell fontFamily="mono">{p.label}</Table.Cell>
                  <Table.Cell>{p.trust_tier || "—"}</Table.Cell>
                  <Table.Cell>
                    <HStack gap={1}>
                      {(p.roles ?? []).length === 0 ? (
                        <Text color="fg.muted" fontSize="sm">
                          council only
                        </Text>
                      ) : (
                        p.roles!.map((r) => (
                          <Badge key={r} variant="subtle">
                            {r}
                          </Badge>
                        ))
                      )}
                    </HStack>
                  </Table.Cell>
                </Table.Row>
              ))}
            </Table.Body>
          </Table.Root>
        </Box>
      )}

      {data.channels.length > 0 && (
        <Box mt={6}>
          <Heading size="md" mb={3}>
            Channels
          </Heading>
          <HStack gap={2}>
            {data.channels.map((c) => (
              <Badge key={c.type} colorPalette="green" variant="subtle">
                {c.type}
              </Badge>
            ))}
          </HStack>
        </Box>
      )}
    </Box>
  );
}

function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Card.Root variant="outline">
      <Card.Body>
        <Heading size="sm" mb={3}>
          {title}
        </Heading>
        <Stack gap={2}>{children}</Stack>
      </Card.Body>
    </Card.Root>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <HStack justify="space-between" fontSize="sm">
      <Text color="fg.muted">{label}</Text>
      <Text fontFamily="mono">{value}</Text>
    </HStack>
  );
}

function yesNo(v: boolean) {
  return v ? "yes" : "no";
}
