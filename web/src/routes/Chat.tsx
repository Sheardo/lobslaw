import { Box, Button, Center, Flex, HStack, Stack, Text, Textarea } from "@chakra-ui/react";
import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { api, streamBotChat } from "../api";
import { Avatar, ErrorPanel, Loading, useLoad } from "../components/ui";
import { botColors } from "../theme";

interface Line {
  from: "you" | string;
  text: string;
  tone?: "normal" | "notice";
}

/** Chat, as the hero surface rather than a box on a page.
 *
 * Full height with a sticky composer, because this is the thing people
 * keep open. The first version put it in the same padded column as
 * every admin screen, which made talking to your assistant feel like
 * filling in a form.
 */
export function Chat() {
  const [params, setParams] = useSearchParams();
  const { data: bots, error, loading } = useLoad(() => api.listBots());
  const [lines, setLines] = useState<Line[]>([]);
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [working, setWorking] = useState(false);
  const [sendError, setSendError] = useState<Error | null>(null);
  const endRef = useRef<HTMLDivElement>(null);

  // Falls through to the first bot rather than "": a chat addressed
  // to nobody has an empty composer placeholder and sends nowhere,
  // which reads as broken rather than as unconfigured.
  const selected =
    params.get("bot") ?? bots?.find((b) => b.is_coordinator)?.id ?? bots?.[0]?.id ?? "";
  const bot = bots?.find((b) => b.id === selected);

  useEffect(() => {
    endRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [lines, working]);

  async function send() {
    const text = draft.trim();
    if (!text || !selected) return;
    setLines((prev) => [...prev, { from: "you", text }]);
    setDraft("");
    setBusy(true);
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
                tone: "notice",
                text: `Needs a confirmation (${String(data.reason ?? "")}). ${String(data.note ?? "")}`,
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

  if (error) return <Box p={8}><ErrorPanel error={error} /></Box>;
  if (loading && !bots) return <Loading />;

  return (
    <Flex direction="column" h="100vh">
      {/* Who you are talking to, pinned. A chat where the other party
          is only identified in a dropdown is one you send the wrong
          message into. */}
      <HStack
        px={6}
        py={3}
        borderBottomWidth="1px"
        borderColor="edge.soft"
        bg="bg.s1"
        gap={3}
        overflowX="auto"
      >
        {(bots ?? []).map((b) => {
          const active = b.id === selected;
          const c = botColors(b.id);
          return (
            <HStack
              key={b.id}
              px={2.5}
              py={1.5}
              gap={2}
              rounded="full"
              cursor="pointer"
              bg={active ? c.muted : "transparent"}
              borderWidth="1px"
              borderColor={active ? c.border : "transparent"}
              _hover={{ bg: active ? c.muted : "bg.s2" }}
              transition="all 120ms"
              onClick={() => {
                setParams({ bot: b.id });
                // A new bot is a new conversation. Carrying the old
                // transcript across would show a history the bot you
                // are now talking to has never seen.
                setLines([]);
                setSendError(null);
              }}
            >
              <Avatar id={b.id} name={b.display_name} size={22} dimmed={!b.enabled} />
              <Text fontSize="13px" fontWeight={active ? "600" : "500"} color={active ? c.text : "fg.mid"}>
                {b.display_name || b.id}
              </Text>
            </HStack>
          );
        })}
      </HStack>

      <Box flex="1" overflowY="auto" px={6} py={6}>
        <Box maxW="760px" mx="auto">
          {lines.length === 0 && !working && (
            <Center flexDirection="column" py={20} textAlign="center">
              <Avatar id={selected || "coordinator"} name={bot?.display_name} size={56} />
              <Text fontWeight="600" fontSize="lg" mt={4}>
                {bot?.display_name || selected}
              </Text>
              <Text color="fg.mid" fontSize="sm" mt={1} maxW="sm">
                {bot?.description || "Ask it something."}
              </Text>
            </Center>
          )}

          <Stack gap={5}>
            {lines.map((line, i) => (
              <Bubble key={i} line={line} name={bots?.find((b) => b.id === line.from)?.display_name} />
            ))}
            {working && (
              <HStack gap={3}>
                <Avatar id={selected} size={30} />
                <HStack gap={1.5} py={2}>
                  {[0, 1, 2].map((d) => (
                    <Box
                      key={d}
                      w="5px"
                      h="5px"
                      rounded="full"
                      bg="fg.low"
                      css={{
                        animation: `blink 1.2s ${d * 0.15}s ease-in-out infinite`,
                        "@keyframes blink": {
                          "0%, 80%, 100%": { opacity: 0.25 },
                          "40%": { opacity: 1 },
                        },
                      }}
                    />
                  ))}
                </HStack>
              </HStack>
            )}
          </Stack>

          {sendError && (
            <Box mt={5}>
              <ErrorPanel error={sendError} />
            </Box>
          )}
          <div ref={endRef} />
        </Box>
      </Box>

      <Box borderTopWidth="1px" borderColor="edge.soft" bg="bg.s1" px={6} py={4}>
        <Flex maxW="760px" mx="auto" gap={3} align="flex-end">
          <Textarea
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder={`Message ${bot?.display_name || selected}…`}
            rows={1}
            resize="none"
            minH="44px"
            maxH="180px"
            bg="bg.s2"
            borderColor="edge.mid"
            rounded="control"
            _focus={{ borderColor: "edge.hard" }}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey) {
                e.preventDefault();
                void send();
              }
            }}
          />
          <Button
            onClick={send}
            loading={busy}
            disabled={!draft.trim() || !selected}
            bg="brand.solid"
            color="#16100C"
            fontWeight="600"
            _hover={{ bg: "brand.hover" }}
            rounded="control"
            h="44px"
          >
            Send
          </Button>
        </Flex>
      </Box>
    </Flex>
  );
}

function Bubble({ line, name }: { line: Line; name?: string }) {
  if (line.from === "you") {
    return (
      <Flex justify="flex-end">
        <Box bg="bg.s3" px={4} py={2.5} rounded="card" maxW="80%">
          <Text fontSize="14px" whiteSpace="pre-wrap" lineHeight="1.65">
            {line.text}
          </Text>
        </Box>
      </Flex>
    );
  }
  const c = botColors(line.from);
  return (
    <HStack align="flex-start" gap={3}>
      <Avatar id={line.from} name={name} size={30} />
      <Box minW={0} flex="1">
        <Text fontSize="12px" fontWeight="600" color={c.text} mb={1}>
          {name || line.from}
        </Text>
        <Text
          fontSize="14px"
          whiteSpace="pre-wrap"
          lineHeight="1.65"
          color={line.tone === "notice" ? "st.cancelled" : "fg.hi"}
        >
          {line.text}
        </Text>
      </Box>
    </HStack>
  );
}
