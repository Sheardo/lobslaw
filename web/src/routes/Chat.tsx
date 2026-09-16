import {
  Box,
  Button,
  Card,
  Flex,
  HStack,
  Heading,
  NativeSelect,
  Spinner,
  Stack,
  Text,
  Textarea,
} from "@chakra-ui/react";
import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import { api, streamBotChat } from "../api";
import { ErrorPanel, Loading, useLoad } from "../components/common";

interface Line {
  from: "you" | string;
  text: string;
}

/** Chat with any bot.
 *
 * The bot picker is the point. /v1/messages reaches the chief and is
 * shared with Telegram and Slack; without a per-bot route every
 * specialist would be something you can configure but never talk to.
 *
 * Streamed, because a bot turn can run tools for a minute and a
 * request that returns nothing until it finishes looks identical to
 * one that has hung — which is how somebody reloads and starts a
 * second turn.
 */
export function Chat() {
  const [params, setParams] = useSearchParams();
  const { data: bots, error, loading } = useLoad(() => api.listBots());
  const [lines, setLines] = useState<Line[]>([]);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [working, setWorking] = useState(false);
  const [sendError, setSendError] = useState<Error | null>(null);

  const selected = params.get("bot") ?? bots?.find((b) => b.is_chief)?.id ?? "";

  async function send() {
    const text = draft.trim();
    if (!text || !selected) return;
    setLines((prev) => [...prev, { from: "you", text }]);
    setDraft("");
    setBusy(true);
    setWorking(false);
    setSendError(null);
    try {
      await streamBotChat(selected, text, (event, data) => {
        switch (event) {
          case "working":
            setWorking(true);
            break;
          case "reply":
            setWorking(false);
            setLines((prev) => [...prev, { from: selected, text: String(data.text ?? "") }]);
            break;
          case "needs_confirmation":
            setWorking(false);
            setLines((prev) => [
              ...prev,
              {
                from: selected,
                text:
                  `That needs a confirmation (${String(data.reason ?? "")}). ` +
                  String(data.note ?? ""),
              },
            ]);
            break;
          case "error":
            setWorking(false);
            setSendError(new Error(String(data.message ?? "the turn failed")));
            break;
        }
      });
    } catch (err) {
      setSendError(err as Error);
    } finally {
      setBusy(false);
      setWorking(false);
    }
  }

  if (error) return <ErrorPanel error={error} />;
  if (loading && !bots) return <Loading />;

  return (
    <Box>
      <Flex align="center" justify="space-between" mb={1} gap={4}>
        <Heading size="lg">Chat</Heading>
        <NativeSelect.Root size="sm" width="56">
          <NativeSelect.Field
            value={selected}
            onChange={(e) => {
              setParams({ bot: e.target.value });
              // A new bot is a new conversation. Carrying the old
              // transcript across would show a history the bot you are
              // now talking to has never seen.
              setLines([]);
            }}
          >
            {(bots ?? []).map((b) => (
              <option key={b.id} value={b.id}>
                {b.display_name || b.id}
                {b.is_chief ? " (chief of staff)" : ""}
              </option>
            ))}
          </NativeSelect.Field>
          <NativeSelect.Indicator />
        </NativeSelect.Root>
      </Flex>
      <Text color="fg.muted" fontSize="sm" mb={5}>
        Ask the chief of staff to create a bot, or talk to a specialist directly.
      </Text>

      <Stack gap={3} mb={4}>
        {lines.map((line, i) => (
          <Card.Root
            key={i}
            variant="subtle"
            alignSelf={line.from === "you" ? "flex-end" : "flex-start"}
            maxW="3xl"
          >
            <Card.Body py={3}>
              <Text fontSize="xs" color="fg.muted" mb={1}>
                {line.from}
              </Text>
              <Text whiteSpace="pre-wrap">{line.text}</Text>
            </Card.Body>
          </Card.Root>
        ))}
        {working && (
          <HStack color="fg.muted" fontSize="sm">
            <Spinner size="xs" />
            <Text>{selected} is working…</Text>
          </HStack>
        )}
      </Stack>

      {sendError && (
        <Box mb={4}>
          <ErrorPanel error={sendError} />
        </Box>
      )}

      <Flex gap={3} align="end">
        <Textarea
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="Make me a devops bot that checks the cluster every morning."
          rows={3}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void send();
            }
          }}
        />
        <Button onClick={send} loading={busy} disabled={!draft.trim() || !selected}>
          Send
        </Button>
      </Flex>
    </Box>
  );
}
