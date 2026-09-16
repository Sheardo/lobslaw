import {
  Badge,
  Box,
  Button,
  Card,
  Field,
  Flex,
  HStack,
  Heading,
  Input,
  NativeSelect,
  Stack,
  Tabs,
  Text,
  Textarea,
} from "@chakra-ui/react";
import { useState } from "react";
import { useParams } from "react-router-dom";
import { api, type Bot, type InboxItem, type InboxKind } from "../api";
import {
  EmptyState,
  ErrorPanel,
  Loading,
  StatusBadge,
  useLoad,
  when,
} from "../components/common";

export function BotDetail() {
  const { botId = "" } = useParams();
  const { data: bot, error, loading, reload } = useLoad(() => api.getBot(botId), [botId]);

  if (error) return <ErrorPanel error={error} />;
  if (loading && !bot) return <Loading />;
  if (!bot) return null;

  return (
    <Box>
      <Flex align="center" justify="space-between" mb={5}>
        <Box>
          <HStack gap={3}>
            <Heading size="lg">{bot.display_name || bot.id}</Heading>
            {bot.is_chief && <Badge colorPalette="purple">chief</Badge>}
            {!bot.enabled && <Badge colorPalette="orange">disabled</Badge>}
          </HStack>
          <Text fontFamily="mono" fontSize="sm" color="fg.muted">
            bot:{bot.id}
          </Text>
        </Box>
      </Flex>

      {/* The inbox is first, not a sub-tab. It is where you see what
          this bot is working on, retry something that failed, and read
          the result of whatever ran at 3am — which is most of why
          somebody opens a bot's page at all. */}
      <Tabs.Root defaultValue="inbox" lazyMount>
        <Tabs.List>
          <Tabs.Trigger value="inbox">Inbox</Tabs.Trigger>
          <Tabs.Trigger value="settings">Settings</Tabs.Trigger>
        </Tabs.List>
        <Tabs.Content value="inbox">
          <Inbox botId={bot.id} />
        </Tabs.Content>
        <Tabs.Content value="settings">
          <Settings bot={bot} onSaved={reload} />
        </Tabs.Content>
      </Tabs.Root>
    </Box>
  );
}

const STATUSES = ["all", "pending", "claimed", "done", "failed", "cancelled"];

function Inbox({ botId }: { botId: string }) {
  const [status, setStatus] = useState("all");
  const [assigning, setAssigning] = useState(false);
  const { data, error, loading, reload } = useLoad(
    () => api.listInbox(botId, status),
    [botId, status],
  );

  return (
    <Stack gap={4} pt={4}>
      <Flex align="center" justify="space-between" gap={3}>
        <NativeSelect.Root size="sm" width="44">
          <NativeSelect.Field value={status} onChange={(e) => setStatus(e.target.value)}>
            {STATUSES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </NativeSelect.Field>
          <NativeSelect.Indicator />
        </NativeSelect.Root>
        <HStack>
          <Button size="sm" variant="outline" onClick={reload} loading={loading}>
            Refresh
          </Button>
          <Button size="sm" onClick={() => setAssigning((v) => !v)}>
            {assigning ? "Cancel" : "Assign work"}
          </Button>
        </HStack>
      </Flex>

      {assigning && (
        <AssignForm
          botId={botId}
          onAssigned={() => {
            setAssigning(false);
            reload();
          }}
        />
      )}

      {error && <ErrorPanel error={error} />}
      {!error && loading && !data && <Loading />}
      {!error && data && data.length === 0 && (
        <EmptyState
          title="Nothing in this queue"
          hint="Assign it something, or let another bot hand it work."
        />
      )}
      {!error &&
        data?.map((item) => (
          <InboxRow key={item.id} botId={botId} item={item} onChanged={reload} />
        ))}
    </Stack>
  );
}

function InboxRow({
  botId,
  item,
  onChanged,
}: {
  botId: string;
  item: InboxItem;
  onChanged: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const [full, setFull] = useState<InboxItem | null>(null);

  async function expand() {
    const next = !open;
    setOpen(next);
    // The listing omits bodies on purpose — sending every body in a
    // hundred-item queue is how a page becomes a megabyte — so the
    // detail is fetched when somebody actually opens one.
    if (next && !full) {
      try {
        setFull(await api.readItem(botId, item.id));
      } catch (err) {
        setError(err as Error);
      }
    }
  }

  async function act(action: "retry" | "cancel") {
    setBusy(true);
    setError(null);
    try {
      await api.actOnItem(botId, item.id, action);
      onChanged();
    } catch (err) {
      setError(err as Error);
    } finally {
      setBusy(false);
    }
  }

  const detail = full ?? item;
  const canRetry = item.status === "failed" || item.status === "cancelled";
  const canCancel = item.status === "pending";

  return (
    <Card.Root variant="outline">
      <Card.Body>
        <Flex align="start" justify="space-between" gap={4}>
          <Box flex="1" cursor="pointer" onClick={expand}>
            <HStack gap={2} mb={1}>
              <StatusBadge status={item.status} />
              <Badge variant="outline">{item.kind}</Badge>
              {item.priority !== 0 && <Badge colorPalette="yellow">p{item.priority}</Badge>}
            </HStack>
            <Text fontWeight="medium">{item.subject || "(no subject)"}</Text>
            <Text fontSize="xs" color="fg.muted">
              from {item.sender} · {when(item.created_at)}
              {item.attempts > 1 && ` · ${item.attempts} attempts`}
            </Text>
          </Box>
          <HStack>
            {canRetry && (
              <Button size="xs" variant="outline" onClick={() => act("retry")} loading={busy}>
                Retry
              </Button>
            )}
            {canCancel && (
              <Button size="xs" variant="outline" onClick={() => act("cancel")} loading={busy}>
                Cancel
              </Button>
            )}
          </HStack>
        </Flex>

        {error && (
          <Box mt={3}>
            <ErrorPanel error={error} />
          </Box>
        )}

        {open && (
          <Stack gap={3} mt={4} pt={4} borderTopWidth="1px">
            <Section label="Body" body={detail.body} />
            {detail.result && <Section label="Result" body={detail.result} />}
            {/* The error stays on a failed item and stays visible.
                A task that vanished quietly is the failure the queue
                exists to prevent. */}
            {detail.error && <Section label="Error" body={detail.error} tone="red.500" />}
          </Stack>
        )}
      </Card.Body>
    </Card.Root>
  );
}

function Section({ label, body, tone }: { label: string; body?: string; tone?: string }) {
  if (!body) return null;
  return (
    <Box>
      <Text fontSize="xs" fontWeight="semibold" color={tone ?? "fg.muted"} mb={1}>
        {label.toUpperCase()}
      </Text>
      <Text fontSize="sm" whiteSpace="pre-wrap" color={tone}>
        {body}
      </Text>
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
    setBusy(true);
    setError(null);
    try {
      await api.assign(botId, { subject: subject.trim(), body: body.trim(), kind, priority });
      setSubject("");
      setBody("");
      onAssigned();
    } catch (err) {
      setError(err as Error);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card.Root variant="outline">
      <Card.Body>
        <Stack gap={4}>
          <Field.Root>
            <Field.Label>Subject</Field.Label>
            <Input
              value={subject}
              onChange={(e) => setSubject(e.target.value)}
              placeholder="Deploy the staging branch"
            />
            <Field.HelperText>Optional — defaults to the first line of the body.</Field.HelperText>
          </Field.Root>

          <Field.Root required>
            <Field.Label>What to do</Field.Label>
            <Textarea
              value={body}
              onChange={(e) => setBody(e.target.value)}
              rows={4}
              placeholder="Deploy the staging branch and report what version is live."
            />
          </Field.Root>

          <HStack gap={4} align="end">
            <Field.Root width="40">
              <Field.Label>Kind</Field.Label>
              <NativeSelect.Root size="sm">
                <NativeSelect.Field
                  value={kind}
                  onChange={(e) => setKind(e.target.value as InboxKind)}
                >
                  {KINDS.map((k) => (
                    <option key={k} value={k}>
                      {k}
                    </option>
                  ))}
                </NativeSelect.Field>
                <NativeSelect.Indicator />
              </NativeSelect.Root>
            </Field.Root>

            <Field.Root width="32">
              <Field.Label>Priority</Field.Label>
              <Input
                type="number"
                size="sm"
                value={priority}
                onChange={(e) => setPriority(Number(e.target.value) || 0)}
              />
              <Field.HelperText>Higher first.</Field.HelperText>
            </Field.Root>
          </HStack>

          {error && <ErrorPanel error={error} />}

          <Button onClick={submit} loading={busy} disabled={!body.trim()} alignSelf="start">
            Assign
          </Button>
        </Stack>
      </Card.Body>
    </Card.Root>
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

  function list(raw: string) {
    return raw
      .split(",")
      .map((s) => s.trim())
      .filter(Boolean);
  }

  async function save() {
    setBusy(true);
    setError(null);
    try {
      await api.updateBot(bot.id, {
        display_name: displayName,
        description,
        instructions,
        tools: list(tools),
        may_message: list(mayMessage),
      });
      onSaved();
    } catch (err) {
      setError(err as Error);
    } finally {
      setBusy(false);
    }
  }

  async function toggleEnabled() {
    setBusy(true);
    setError(null);
    try {
      await api.updateBot(bot.id, { enabled: !bot.enabled });
      onSaved();
    } catch (err) {
      setError(err as Error);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Stack gap={4} pt={4} maxW="3xl">
      <Field.Root>
        <Field.Label>Display name</Field.Label>
        <Input value={displayName} onChange={(e) => setDisplayName(e.target.value)} />
      </Field.Root>

      <Field.Root>
        <Field.Label>Description</Field.Label>
        <Input value={description} onChange={(e) => setDescription(e.target.value)} />
      </Field.Root>

      <Field.Root>
        <Field.Label>Instructions</Field.Label>
        <Textarea
          value={instructions}
          onChange={(e) => setInstructions(e.target.value)}
          rows={8}
        />
        <Field.HelperText>
          Rides on every turn this bot takes. Keep it about the role, not a task.
        </Field.HelperText>
      </Field.Root>

      <Field.Root>
        <Field.Label>Tools</Field.Label>
        <Input
          value={tools}
          onChange={(e) => setTools(e.target.value)}
          placeholder="web_search, fetch_url, memory_search"
          fontFamily="mono"
        />
        <Field.HelperText>
          Comma-separated. Leave empty for every tool this node has. A tool left out is
          one this bot is never even shown, so this is how you limit what it can do.
        </Field.HelperText>
      </Field.Root>

      <Field.Root>
        <Field.Label>Can message</Field.Label>
        <Input
          value={mayMessage}
          onChange={(e) => setMayMessage(e.target.value)}
          placeholder="engineering, devops"
          fontFamily="mono"
        />
        <Field.HelperText>
          Comma-separated bot ids this one may ask or hand work to. Loops are refused —
          the error names the path.
        </Field.HelperText>
      </Field.Root>

      {error && <ErrorPanel error={error} />}

      <HStack>
        <Button onClick={save} loading={busy}>
          Save
        </Button>
        {/* The chief answers your Telegram and Slack messages, so the
            API refuses to delete it. Not offering the switch beats
            offering one that fails. */}
        {!bot.is_chief && (
          <Button variant="outline" onClick={toggleEnabled} loading={busy}>
            {bot.enabled ? "Disable" : "Enable"}
          </Button>
        )}
      </HStack>
    </Stack>
  );
}
