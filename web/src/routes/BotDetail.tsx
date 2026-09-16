import {
  Box, Button, Field, Flex, HStack, Input, NativeSelect, Stack, Tabs, Text, Textarea,
} from "@chakra-ui/react";
import { useState } from "react";
import { Link as RouterLink, useParams } from "react-router-dom";
import { api, type Bot, type InboxItem, type InboxKind, type TranscriptMessage } from "../api";
import { Page } from "../App";
import { Avatar, Empty, ErrorPanel, Loading, Panel, Status, useLoad, when } from "../components/ui";
import { botColors } from "../theme";

export function BotDetail({ onChanged }: { onChanged: () => void }) {
  const { botId = "" } = useParams();
  const { data: bot, error, loading, reload } = useLoad(() => api.getBot(botId), [botId]);

  if (error) return <Page title="Bot"><ErrorPanel error={error} /></Page>;
  if (loading && !bot) return <Loading />;
  if (!bot) return null;

  const c = botColors(bot.id);

  return (
    <Page
      title={
        <HStack gap={3}>
          <Avatar id={bot.id} name={bot.display_name} size={36} dimmed={!bot.enabled} />
          <Box>
            <Text fontSize="22px" fontWeight="600" letterSpacing="-0.02em" color={c.text}>
              {bot.display_name || bot.id}
            </Text>
          </Box>
          {bot.is_coordinator && (
            <Text fontSize="11px" color="brand.solid" borderWidth="1px" borderColor="brand.solid"
              px={2} py={0.5} rounded="full" fontWeight="600">
              coordinator
            </Text>
          )}
          {!bot.enabled && (
            <Text fontSize="11px" color="st.cancelled" borderWidth="1px"
              borderColor="st.cancelled" px={2} py={0.5} rounded="full" fontWeight="600">
              disabled
            </Text>
          )}
        </HStack>
      }
      subtitle={bot.description || `bot:${bot.id}`}
    >
      <Tabs.Root defaultValue="inbox" lazyMount variant="line">
        <Tabs.List borderColor="edge.soft">
          {/* Explicit colours: the default recipe resolved the ACTIVE
              trigger to a dark foreground, so the selected tab was
              invisible against the dark canvas. */}
          <Tabs.Trigger value="inbox" fontSize="13px" color="fg.mid"
            _selected={{ color: "fg.hi", fontWeight: "600" }}>Inbox</Tabs.Trigger>
          <Tabs.Trigger value="settings" fontSize="13px" color="fg.mid"
            _selected={{ color: "fg.hi", fontWeight: "600" }}>Settings</Tabs.Trigger>
        </Tabs.List>
        <Tabs.Content value="inbox" pt={5}>
          <Inbox botId={bot.id} />
        </Tabs.Content>
        <Tabs.Content value="settings" pt={5}>
          <Settings bot={bot} onSaved={() => { reload(); onChanged(); }} />
        </Tabs.Content>
      </Tabs.Root>
    </Page>
  );
}

const STATUSES = ["all", "pending", "claimed", "done", "failed", "cancelled"];

function Inbox({ botId }: { botId: string }) {
  const [status, setStatus] = useState("all");
  const [assigning, setAssigning] = useState(false);
  const { data, error, loading, reload } = useLoad(
    () => api.listInbox(botId, status), [botId, status],
  );

  return (
    <Stack gap={3}>
      <Flex align="center" justify="space-between" gap={3}>
        <HStack gap={1}>
          {STATUSES.map((s) => (
            <Box
              key={s}
              px={2.5} py={1} rounded="full" cursor="pointer" fontSize="12px"
              fontWeight={status === s ? "600" : "500"}
              color={status === s ? "fg.hi" : "fg.low"}
              bg={status === s ? "bg.s3" : "transparent"}
              _hover={{ color: "fg.hi" }}
              transition="all 120ms"
              onClick={() => setStatus(s)}
            >
              {s}
            </Box>
          ))}
        </HStack>
        <HStack gap={2}>
          <Button size="xs" variant="ghost" color="fg.mid" onClick={reload} loading={loading}>
            Refresh
          </Button>
          <Button size="xs" bg="brand.solid" color="#16100C" fontWeight="600" _hover={{ bg: "brand.hover" }}
            rounded="control" onClick={() => setAssigning((v) => !v)}>
            {assigning ? "Cancel" : "Assign work"}
          </Button>
        </HStack>
      </Flex>

      {assigning && (
        <AssignForm botId={botId} onAssigned={() => { setAssigning(false); reload(); }} />
      )}

      {error && <ErrorPanel error={error} />}
      {!error && loading && !data && <Loading />}
      {!error && data?.length === 0 && (
        <Empty title="Nothing in this queue"
          hint="Assign it something, or let another bot hand it work." />
      )}
      {!error && data?.map((item) => (
        <Row key={item.id} botId={botId} item={item} onChanged={reload} />
      ))}
    </Stack>
  );
}

function Row({ botId, item, onChanged }: { botId: string; item: InboxItem; onChanged: () => void }) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [full, setFull] = useState<InboxItem | null>(null);

  async function expand() {
    const next = !open;
    setOpen(next);
    // The listing omits bodies on purpose — sending every body in a
    // hundred-item queue is how a page becomes a megabyte.
    if (next && !full) {
      try { setFull(await api.readItem(botId, item.id)); }
      catch (err) { setError(err as Error); }
    }
  }

  async function act(action: "retry" | "cancel") {
    setBusy(true); setError(null);
    try { await api.actOnItem(botId, item.id, action); onChanged(); }
    catch (err) { setError(err as Error); }
    finally { setBusy(false); }
  }

  const detail = full ?? item;
  const canRetry = item.status === "failed" || item.status === "cancelled";
  const canCancel = item.status === "pending";

  return (
    <Panel px={4} py={3.5} _hover={{ borderColor: "edge.mid" }} transition="border-color 120ms">
      <Flex gap={4} align="flex-start">
        <Box flex="1" minW={0} cursor="pointer" onClick={expand}>
          <HStack gap={2} mb={1}>
            <Status status={item.status} />
            <Text fontSize="11px" color="fg.low">{item.kind}</Text>
            {item.priority !== 0 && (
              <Text fontSize="11px" color="st.cancelled">p{item.priority}</Text>
            )}
          </HStack>
          <Text fontSize="14px" fontWeight="500">{item.subject || "(no subject)"}</Text>
          <Text fontSize="11px" color="fg.low" mt={0.5}>
            from {item.sender} · {when(item.created_at)}
            {item.attempts > 1 && ` · ${item.attempts} attempts`}
          </Text>
        </Box>
        <HStack gap={2}>
          {canRetry && (
            <Button size="xs" variant="outline" borderColor="edge.mid" rounded="control"
              onClick={() => act("retry")} loading={busy}>Retry</Button>
          )}
          {canCancel && (
            <Button size="xs" variant="ghost" color="fg.mid"
              onClick={() => act("cancel")} loading={busy}>Cancel</Button>
          )}
        </HStack>
      </Flex>

      {error && <Box mt={3}><ErrorPanel error={error} /></Box>}

      {open && (
        <Stack gap={4} mt={4} pt={4} borderTopWidth="1px" borderColor="edge.soft">
          <Section label="Asked" body={detail.body} />
          {detail.result && <Section label="Result" body={detail.result} />}
          {/* A failed item keeps its error, visibly. A task that
              vanished quietly is the failure the queue exists to
              prevent. */}
          {detail.error && <Section label="Error" body={detail.error} tone="st.failed" />}
          {detail.session_id && <Transcript sessionId={detail.session_id} />}
        </Stack>
      )}
    </Panel>
  );
}

function Transcript({ sessionId }: { sessionId: string }) {
  const [open, setOpen] = useState(false);
  const [messages, setMessages] = useState<TranscriptMessage[] | null>(null);
  const [error, setError] = useState<Error | null>(null);

  async function toggle() {
    const next = !open;
    setOpen(next);
    if (next && !messages) {
      try { setMessages(await api.transcript(sessionId)); }
      catch (err) { setError(err as Error); }
    }
  }

  return (
    <Box>
      <Text fontSize="12px" color="fg.low" cursor="pointer" _hover={{ color: "fg.mid" }}
        onClick={toggle}>
        {open ? "▾" : "▸"} what the bot did
      </Text>
      {error && <Box mt={2}><ErrorPanel error={error} /></Box>}
      {open && messages && (
        <Stack gap={3} mt={3} pl={3} borderLeftWidth="1px" borderColor="edge.soft">
          {messages.map((m) => (
            <Box key={m.seq}>
              <Text fontSize="10px" color="fg.low" textTransform="uppercase"
                letterSpacing="0.06em" fontWeight="600">
                {m.role}{m.tool_calls ? ` · ${m.tool_calls} tool calls` : ""}
              </Text>
              <Text fontSize="13px" whiteSpace="pre-wrap" color="fg.mid" mt={0.5}>
                {m.content || "(tool calls only)"}
              </Text>
            </Box>
          ))}
        </Stack>
      )}
    </Box>
  );
}

function Section({ label, body, tone }: { label: string; body?: string; tone?: string }) {
  if (!body) return null;
  return (
    <Box>
      <Text fontSize="10px" fontWeight="600" letterSpacing="0.06em"
        color={tone ?? "fg.low"} textTransform="uppercase" mb={1.5}>
        {label}
      </Text>
      <Text fontSize="13px" whiteSpace="pre-wrap" lineHeight="1.7"
        color={tone ?? "fg.mid"}>{body}</Text>
    </Box>
  );
}

const KINDS: InboxKind[] = ["task", "question", "fyi"];

function AssignForm({ botId, onAssigned }: { botId: string; onAssigned: () => void }) {
  const [subject, setSubject] = useState("");
  const [body, setBody] = useState("");
  const [kind, setKind] = useState<InboxKind>("task");
  const [priority, setPriority] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error | null>(null);

  async function submit() {
    setBusy(true); setError(null);
    try {
      await api.assign(botId, { subject: subject.trim(), body: body.trim(), kind, priority });
      setSubject(""); setBody(""); onAssigned();
    } catch (err) { setError(err as Error); }
    finally { setBusy(false); }
  }

  const input = { bg: "bg.s2", borderColor: "edge.mid", rounded: "control" } as const;

  return (
    <Panel p={4}>
      <Stack gap={3}>
        <Input {...input} value={subject} onChange={(e) => setSubject(e.target.value)}
          placeholder="Subject (optional — defaults to the first line)" size="sm" />
        <Textarea {...input} value={body} onChange={(e) => setBody(e.target.value)}
          rows={3} placeholder="Deploy the staging branch and report what version is live." />
        <HStack gap={3}>
          <NativeSelect.Root size="sm" width="28">
            <NativeSelect.Field value={kind} onChange={(e) => setKind(e.target.value as InboxKind)}
              bg="bg.s2" borderColor="edge.mid" rounded="control">
              {KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
            </NativeSelect.Field>
            <NativeSelect.Indicator />
          </NativeSelect.Root>
          <Input {...input} type="number" size="sm" width="24" value={priority}
            onChange={(e) => setPriority(Number(e.target.value) || 0)} placeholder="priority" />
          <Button size="sm" onClick={submit} loading={busy} disabled={!body.trim()}
            bg="brand.solid" color="#16100C" fontWeight="600" _hover={{ bg: "brand.hover" }} rounded="control">
            Assign
          </Button>
        </HStack>
        {error && <ErrorPanel error={error} />}
      </Stack>
    </Panel>
  );
}

function Settings({ bot, onSaved }: { bot: Bot; onSaved: () => void }) {
  const [displayName, setDisplayName] = useState(bot.display_name);
  const [description, setDescription] = useState(bot.description);
  const [instructions, setInstructions] = useState(bot.instructions);
  const [tools, setTools] = useState(bot.tools.join(", "));
  const [mayMessage, setMayMessage] = useState(bot.may_message.join(", "));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error | null>(null);

  const list = (raw: string) => raw.split(",").map((s) => s.trim()).filter(Boolean);
  const input = { bg: "bg.s2", borderColor: "edge.mid", rounded: "control" } as const;

  async function save(patch?: Partial<Bot>) {
    setBusy(true); setError(null);
    try {
      await api.updateBot(bot.id, patch ?? {
        display_name: displayName, description, instructions,
        tools: list(tools), may_message: list(mayMessage),
      });
      onSaved();
    } catch (err) { setError(err as Error); }
    finally { setBusy(false); }
  }

  return (
    <Panel p={5} maxW="700px">
      <Stack gap={5}>
        <Field.Root>
          <Field.Label fontSize="13px">Display name</Field.Label>
          <Input {...input} value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
        </Field.Root>
        <Field.Root>
          <Field.Label fontSize="13px">Description</Field.Label>
          <Input {...input} value={description} onChange={(e) => setDescription(e.target.value)} />
        </Field.Root>
        <Field.Root>
          <Field.Label fontSize="13px">Instructions</Field.Label>
          <Textarea {...input} value={instructions} rows={8}
            onChange={(e) => setInstructions(e.target.value)} />
          <Field.HelperText fontSize="xs" color="fg.low">
            Rides on every turn this bot takes. The role, not a task.
          </Field.HelperText>
        </Field.Root>
        <Field.Root>
          <Field.Label fontSize="13px">Tools</Field.Label>
          <Input {...input} value={tools} fontFamily="mono" fontSize="13px"
            onChange={(e) => setTools(e.target.value)}
            placeholder="web_search, fetch_url, memory_search" />
          <Field.HelperText fontSize="xs" color="fg.low">
            Empty means every tool this node has. A tool left out is one this bot is never
            even shown — that is how you limit what it can do.
          </Field.HelperText>
        </Field.Root>
        <Field.Root>
          <Field.Label fontSize="13px">Can message</Field.Label>
          <Input {...input} value={mayMessage} fontFamily="mono" fontSize="13px"
            onChange={(e) => setMayMessage(e.target.value)} placeholder="engineering, devops" />
          <Field.HelperText fontSize="xs" color="fg.low">
            Loops are refused — the error names the path.
          </Field.HelperText>
        </Field.Root>

        {error && <ErrorPanel error={error} />}

        <HStack>
          <Button onClick={() => save()} loading={busy} bg="brand.solid" color="#16100C" fontWeight="600"
            _hover={{ bg: "brand.hover" }} rounded="control">Save</Button>
          {/* The coordinator answers your Telegram messages and the API
              refuses to remove it, so the control is absent rather
              than present and failing. */}
          {!bot.is_coordinator && (
            <Button variant="outline" borderColor="edge.mid" rounded="control" loading={busy}
              onClick={() => save({ enabled: !bot.enabled })}>
              {bot.enabled ? "Disable" : "Enable"}
            </Button>
          )}
          <Box flex="1" />
          <RouterLink to={`/chat?bot=${bot.id}`}>
            <Button variant="ghost" color="fg.mid" size="sm">Chat →</Button>
          </RouterLink>
        </HStack>
      </Stack>
    </Panel>
  );
}
