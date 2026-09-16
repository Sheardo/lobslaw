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
  SimpleGrid,
  Stack,
  Text,
  Textarea,
} from "@chakra-ui/react";
import { useState } from "react";
import { Link as RouterLink } from "react-router-dom";
import { api, type Bot } from "../api";
import { EmptyState, ErrorPanel, Loading, useLoad } from "../components/common";

export function Bots() {
  const { data, error, loading, reload } = useLoad(() => api.listBots());
  const [creating, setCreating] = useState(false);

  return (
    <Box>
      <Flex align="center" justify="space-between" mb={5}>
        <Box>
          <Heading size="lg">Bots</Heading>
          <Text color="fg.muted" fontSize="sm">
            The chief of staff and the specialists it works with.
          </Text>
        </Box>
        <Button size="sm" onClick={() => setCreating((v) => !v)}>
          {creating ? "Cancel" : "New bot"}
        </Button>
      </Flex>

      {creating && (
        <Box mb={6}>
          <NewBotForm
            onCreated={() => {
              setCreating(false);
              reload();
            }}
          />
        </Box>
      )}

      {error && <ErrorPanel error={error} />}
      {!error && loading && !data && <Loading />}
      {!error && data && data.length === 0 && (
        <EmptyState title="No bots yet" hint="The chief of staff is seeded at first boot." />
      )}
      {!error && data && data.length > 0 && (
        <SimpleGrid columns={{ base: 1, md: 2 }} gap={4}>
          {data.map((bot) => (
            <BotCard key={bot.id} bot={bot} />
          ))}
        </SimpleGrid>
      )}
    </Box>
  );
}

function BotCard({ bot }: { bot: Bot }) {
  return (
    <Card.Root
      variant="outline"
      // A disabled bot is dimmed rather than hidden. Hiding it would
      // make "where did my devops bot go" a question with no answer on
      // the page that is supposed to answer it.
      opacity={bot.enabled ? 1 : 0.6}
    >
      <Card.Body>
        <HStack justify="space-between" align="start" mb={2}>
          <Box>
            <RouterLink to={`/bots/${bot.id}`}>
              <Heading size="md" textDecoration="underline">
                {bot.display_name || bot.id}
              </Heading>
            </RouterLink>
            <Text fontFamily="mono" fontSize="xs" color="fg.muted">
              {bot.id}
            </Text>
          </Box>
          <HStack gap={2}>
            {bot.is_chief && <Badge colorPalette="purple">chief</Badge>}
            {!bot.enabled && <Badge colorPalette="orange">disabled</Badge>}
          </HStack>
        </HStack>

        <Text fontSize="sm" color="fg.muted" mb={3}>
          {bot.description || "No description."}
        </Text>

        <Stack gap={1} fontSize="xs" color="fg.muted">
          <Text>
            Tools:{" "}
            {bot.tools.length === 0 ? "everything on this node" : bot.tools.join(", ")}
          </Text>
          {bot.may_message.length > 0 && <Text>Can message: {bot.may_message.join(", ")}</Text>}
        </Stack>
      </Card.Body>
    </Card.Root>
  );
}

function NewBotForm({ onCreated }: { onCreated: () => void }) {
  const [id, setId] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [description, setDescription] = useState("");
  const [instructions, setInstructions] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error | null>(null);

  async function submit() {
    setBusy(true);
    setError(null);
    try {
      await api.createBot({
        id: id.trim(),
        display_name: displayName.trim(),
        description: description.trim(),
        instructions: instructions.trim(),
      });
      onCreated();
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
          <Field.Root required>
            <Field.Label>Id</Field.Label>
            <Input
              value={id}
              onChange={(e) => setId(e.target.value)}
              placeholder="engineering"
              fontFamily="mono"
            />
            <Field.HelperText>
              Lowercase letters, digits and hyphens. This becomes the bot's identity and
              cannot be changed afterwards.
            </Field.HelperText>
          </Field.Root>

          <Field.Root required>
            <Field.Label>Display name</Field.Label>
            <Input
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="Engineering"
            />
          </Field.Root>

          <Field.Root>
            <Field.Label>Description</Field.Label>
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Builds and ships the product."
            />
            <Field.HelperText>
              One line. Other bots read this to decide who to hand work to.
            </Field.HelperText>
          </Field.Root>

          <Field.Root required>
            <Field.Label>Instructions</Field.Label>
            <Textarea
              value={instructions}
              onChange={(e) => setInstructions(e.target.value)}
              placeholder="You are the engineer for XYZ. You own the build, the deploy pipeline and the cluster."
              rows={5}
            />
            <Field.HelperText>
              Its standing brief — what it is for and how it should work. This rides on
              every turn it takes, so keep it about the role rather than a task.
            </Field.HelperText>
          </Field.Root>

          {error && <ErrorPanel error={error} />}

          <HStack>
            <Button onClick={submit} loading={busy} disabled={!id || !displayName || !instructions}>
              Create
            </Button>
            <Text fontSize="sm" color="fg.muted">
              It starts with every tool this node has. Narrow that on its page.
            </Text>
          </HStack>
        </Stack>
      </Card.Body>
    </Card.Root>
  );
}
