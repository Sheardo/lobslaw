import {
  Box,
  Button,
  Card,
  Flex,
  HStack,
  Heading,
  Stack,
  Text,
  Textarea,
} from "@chakra-ui/react";
import { useState } from "react";
import { api } from "../api";
import { ErrorPanel } from "../components/common";

interface Line {
  from: "you" | "assistant";
  text: string;
}

/** Chat with the chief of staff.
 *
 * Deliberately narrow: this is the same /v1/messages endpoint every
 * other channel uses, so what you get here is what you would get on
 * Telegram. Per-bot chat threads would need a session dimension the
 * transcript store does not have yet, and inventing one in the browser
 * would produce a history the node does not agree with.
 */
export function Chat() {
  const [lines, setLines] = useState<Line[]>([]);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error | null>(null);

  async function send() {
    const text = draft.trim();
    if (!text) return;
    setLines((prev) => [...prev, { from: "you", text }]);
    setDraft("");
    setBusy(true);
    setError(null);
    try {
      const res = await api.sendMessage(text);
      setLines((prev) => [
        ...prev,
        {
          from: "assistant",
          text:
            res.reply ??
            (res.needs_confirmation
              ? "That needs a confirmation. Approve it on the channel you usually use."
              : "(no reply)"),
        },
      ]);
    } catch (err) {
      setError(err as Error);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Box>
      <Heading size="lg" mb={1}>
        Chat
      </Heading>
      <Text color="fg.muted" fontSize="sm" mb={5}>
        The same conversation as Telegram or Slack — ask the chief of staff to create a
        bot, or to tell you what the team has been doing.
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
      </Stack>

      {error && (
        <Box mb={4}>
          <ErrorPanel error={error} />
        </Box>
      )}

      <Flex gap={3} align="end">
        <Textarea
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="Make me a devops bot that checks the cluster every morning."
          rows={3}
          onKeyDown={(e) => {
            // Enter sends, shift-enter newlines — the convention every
            // chat app has, and getting it backwards makes the box feel
            // broken before anybody reads a hint.
            if (e.key === "Enter" && !e.shiftKey) {
              e.preventDefault();
              void send();
            }
          }}
        />
        <HStack>
          <Button onClick={send} loading={busy} disabled={!draft.trim()}>
            Send
          </Button>
        </HStack>
      </Flex>
    </Box>
  );
}
