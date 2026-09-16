import { Box, HStack, SimpleGrid, Stack, Text } from "@chakra-ui/react";
import { api } from "../api";
import { Page } from "../App";
import { ErrorPanel, Loading, Panel, useLoad } from "../components/ui";

/** What this node believes about itself.
 *
 * Read-only. Editing configuration from a browser would mean a running
 * node rewriting its own config.toml, and every "this section needs a
 * restart" caveat becomes a race somebody triggers by clicking Save.
 *
 * The node assembles this from an allowlist rather than dumping its
 * config, so what is absent here is absent deliberately — endpoints
 * and credentials among it.
 */
export function Config() {
  const { data, error, loading } = useLoad(() => api.config());

  if (error) return <Page title="Config"><ErrorPanel error={error} /></Page>;
  if (loading && !data) return <Loading />;
  if (!data) return null;

  const exposed = !data.gateway.require_auth &&
    data.gateway.bind_address !== "127.0.0.1" && data.gateway.bind_address !== "localhost";

  return (
    <Page
      title="Config"
      subtitle={`${data.node_id}${data.version ? ` · ${data.version}` : ""} · ${data.functions.join(", ")}`}
    >
      {exposed && (
        <Panel px={4} py={3} mb={4} borderLeftWidth="2px" borderLeftColor="st.failed">
          <Text fontSize="13px" fontWeight="600" color="st.failed" mb={1}>
            This console is unauthenticated
          </Text>
          <Text fontSize="13px" color="fg.mid">
            Anyone who can reach {data.gateway.bind_address || "this node"} can rewrite your
            bots. Set <Mono>[auth] require_auth</Mono> and{" "}
            <Mono>[gateway.ui] token_ref</Mono>, or bind 127.0.0.1.
          </Text>
        </Panel>
      )}

      <SimpleGrid columns={{ base: 1, md: 2 }} gap={3}>
        <Card title="Gateway">
          <Row k="Bind" v={data.gateway.bind_address || "every interface"} />
          <Row k="Port" v={String(data.gateway.http_port)} />
          <Row k="Requires auth" v={data.gateway.require_auth ? "yes" : "no"}
            tone={data.gateway.require_auth ? undefined : "st.cancelled"} />
          <Row k="Console login" v={data.gateway.login_configured ? "configured" : "none"} />
          <Row k="Queue mode" v={data.gateway.queue_mode || "serial"} />
          <Row k="Timezone" v={data.gateway.default_timezone || "UTC"} />
        </Card>

        <Card title="Bots">
          <Row k="Queue depth limit" v={String(data.bots.max_pending)} />
          <Row k="Bots work their queues" v={data.bots.drain_enabled ? "yes" : "no"}
            tone={data.bots.drain_enabled ? undefined : "st.cancelled"} />
        </Card>

        <Card title="Memory">
          <Row k="Enabled" v={data.memory.enabled ? "yes" : "no"} />
          <Row k="Dream schedule" v={data.memory.dream_schedule || "default"} />
          {/* The vector space the corpus is in — the one thing an
              operator debugging bad recall actually needs. */}
          <Row k="Embedding model"
            v={data.memory.embedding_model || "none — recall is lexical"} />
        </Card>

        <Card title="Compute">
          <Row k="Tool calls per turn" v={String(data.compute.max_tool_calls_per_turn)} />
          <Row k="Self-learning" v={data.compute.self_learning_mode || "off"} />
          <Row k="Channels" v={data.channels.map((c) => c.type).join(", ") || "none"} />
        </Card>
      </SimpleGrid>

      {data.compute.providers.length > 0 && (
        <Box mt={6}>
          <Text fontSize="10px" fontWeight="600" letterSpacing="0.08em" color="fg.low"
            textTransform="uppercase" mb={2}>
            Providers
          </Text>
          <Text fontSize="13px" color="fg.mid" mb={3}>
            Labels and roles only — no endpoints, no model names, no credentials.
          </Text>
          <Stack gap={2}>
            {data.compute.providers.map((p) => (
              <Panel key={p.label} px={4} py={3}>
                <HStack justify="space-between">
                  <HStack gap={3}>
                    <Text fontFamily="mono" fontSize="13px" fontWeight="600">{p.label}</Text>
                    {p.trust_tier && (
                      <Text fontSize="11px" color="fg.low">{p.trust_tier}</Text>
                    )}
                  </HStack>
                  <HStack gap={2}>
                    {(p.roles ?? []).length === 0 ? (
                      <Text fontSize="11px" color="fg.low">council only</Text>
                    ) : (
                      p.roles!.map((r) => (
                        <Text key={r} fontSize="11px" color="brand.solid" bg="brand.dim"
                          px={2} py={0.5} rounded="full" fontWeight="600">{r}</Text>
                      ))
                    )}
                  </HStack>
                </HStack>
              </Panel>
            ))}
          </Stack>
        </Box>
      )}
    </Page>
  );
}

function Card({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Panel p={4}>
      <Text fontSize="10px" fontWeight="600" letterSpacing="0.08em" color="fg.low"
        textTransform="uppercase" mb={3}>
        {title}
      </Text>
      <Stack gap={2}>{children}</Stack>
    </Panel>
  );
}

function Row({ k, v, tone }: { k: string; v: string; tone?: string }) {
  return (
    <HStack justify="space-between" fontSize="13px" gap={4}>
      <Text color="fg.mid">{k}</Text>
      <Text fontFamily="mono" fontSize="12px" color={tone ?? "fg.hi"} textAlign="right">{v}</Text>
    </HStack>
  );
}

function Mono({ children }: { children: React.ReactNode }) {
  return (
    <Text as="span" fontFamily="mono" fontSize="12px" bg="bg.s3" px={1} rounded="sm">
      {children}
    </Text>
  );
}
