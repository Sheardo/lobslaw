import { Box, Button, Field, HStack, Input, Stack, Text, Textarea } from "@chakra-ui/react";
import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "../api";
import { Page } from "../App";
import { Avatar, ErrorPanel, Panel } from "../components/ui";

export function NewBot({ onCreated }: { onCreated: () => void }) {
  const nav = useNavigate();
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
      const bot = await api.createBot({
        id: id.trim(),
        display_name: displayName.trim() || id.trim(),
        description: description.trim(),
        instructions: instructions.trim(),
      });
      onCreated();
      nav(`/bots/${bot.id}`);
    } catch (err) {
      setError(err as Error);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Page title="New bot" subtitle="A specialist with its own memory, tools and queue.">
      <Panel p={6} maxW="640px">
        <Stack gap={5}>
          {/* The avatar updates as they type. It is the fastest way to
              convey that a bot has an identity rather than a row. */}
          <HStack gap={3}>
            <Avatar id={id || "new"} name={displayName || id} size={44} />
            <Box>
              <Text fontWeight="600">{displayName || id || "Unnamed"}</Text>
              <Text fontSize="xs" color="fg.low" fontFamily="mono">
                bot:{id || "…"}
              </Text>
            </Box>
          </HStack>

          <Field.Root required>
            <Field.Label fontSize="13px">Id</Field.Label>
            <Input
              value={id}
              onChange={(e) => setId(e.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ""))}
              placeholder="engineering"
              fontFamily="mono"
              bg="bg.s2"
              borderColor="edge.mid"
              rounded="control"
            />
            <Field.HelperText fontSize="xs" color="fg.low">
              Lowercase, hyphens allowed. This becomes the bot&rsquo;s identity and cannot
              be changed.
            </Field.HelperText>
          </Field.Root>

          <Field.Root>
            <Field.Label fontSize="13px">Display name</Field.Label>
            <Input
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="Engineering"
              bg="bg.s2"
              borderColor="edge.mid"
              rounded="control"
            />
          </Field.Root>

          <Field.Root>
            <Field.Label fontSize="13px">Description</Field.Label>
            <Input
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="Builds and ships the product."
              bg="bg.s2"
              borderColor="edge.mid"
              rounded="control"
            />
            <Field.HelperText fontSize="xs" color="fg.low">
              One line. Other bots read this to decide who to hand work to.
            </Field.HelperText>
          </Field.Root>

          <Field.Root required>
            <Field.Label fontSize="13px">Instructions</Field.Label>
            <Textarea
              value={instructions}
              onChange={(e) => setInstructions(e.target.value)}
              placeholder="You are the engineer for XYZ. You own the build, the deploy pipeline and the cluster."
              rows={6}
              bg="bg.s2"
              borderColor="edge.mid"
              rounded="control"
            />
            <Field.HelperText fontSize="xs" color="fg.low">
              Its standing brief — the role, not a task. This rides on every turn it takes.
            </Field.HelperText>
          </Field.Root>

          {error && <ErrorPanel error={error} />}

          <HStack>
            <Button
              onClick={submit}
              loading={busy}
              disabled={!id || !instructions}
              bg="brand.solid"
              color="#16100C"
              _hover={{ bg: "brand.hover" }}
              rounded="control"
            >
              Create
            </Button>
            <Text fontSize="xs" color="fg.low">
              Starts with every tool this node has. Narrow that on its page.
            </Text>
          </HStack>
        </Stack>
      </Panel>
    </Page>
  );
}
