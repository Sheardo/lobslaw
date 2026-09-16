import { Box, Button, Flex, HStack, Stack, Text } from "@chakra-ui/react";
import { useEffect } from "react";
import { Link as RouterLink } from "react-router-dom";
import { api, type InboxItem } from "../api";
import { Page } from "../App";
import { Avatar, Empty, ErrorPanel, Loading, Panel, Status, useLoad, when } from "../components/ui";
import { botColors } from "../theme";

/** What the team is doing, newest first.
 *
 * Built from the inboxes rather than from sessions, because the
 * question this answers is "what is the team doing" — and a session
 * index answers "what conversations exist", which stops being the same
 * thing the moment bots work items nobody chatted about.
 */
export function Activity() {
  const { data, error, loading, reload } = useLoad(() => api.activity(100));

  // A queue that only updates when you press a button is a queue you
  // stop believing. Ten seconds is slow enough to be free and fast
  // enough that a drain finishing feels live.
  useEffect(() => {
    const t = setInterval(reload, 10_000);
    return () => clearInterval(t);
  }, [reload]);

  const working = (data ?? []).filter((i) => i.status === "claimed").length;
  const queued = (data ?? []).filter((i) => i.status === "pending").length;

  return (
    <Page
      title="Activity"
      subtitle={
        data
          ? `${working} working · ${queued} queued · ${data.length} total`
          : "Every bot's queue."
      }
      action={
        <Button size="xs" variant="ghost" color="fg.mid" onClick={reload} loading={loading}>
          Refresh
        </Button>
      }
    >
      {error && <ErrorPanel error={error} />}
      {!error && loading && !data && <Loading />}
      {!error && data?.length === 0 && (
        <Empty
          title="Nothing has happened yet"
          hint="Assign a bot some work from its page, or ask the coordinator to."
        />
      )}
      {!error && data && data.length > 0 && (
        <Stack gap={2}>
          {data.map((item) => (
            <Row key={`${item.recipient}/${item.id}`} item={item} />
          ))}
        </Stack>
      )}
    </Page>
  );
}

function Row({ item }: { item: InboxItem }) {
  const c = botColors(item.recipient);
  const outcome = item.error || item.result;

  return (
    <RouterLink to={`/bots/${item.recipient}`}>
      <Panel
        px={4}
        py={3.5}
        _hover={{ borderColor: "edge.mid", bg: "bg.s2" }}
        transition="all 120ms"
        // A hairline in the bot's colour down the left edge. Cheaper
        // to scan than any badge: you find the devops rows without
        // reading a single word.
        borderLeftWidth="2px"
        borderLeftColor={c.border}
      >
        <Flex gap={3} align="flex-start">
          <Avatar id={item.recipient} size={28} />
          <Box flex="1" minW={0}>
            <HStack gap={2} mb={0.5}>
              <Text fontSize="13px" fontWeight="600" color={c.text}>
                {item.recipient}
              </Text>
              <Text fontSize="11px" color="fg.low">
                {item.kind}
                {item.sender && item.sender !== "operator" ? ` from ${item.sender}` : ""}
              </Text>
            </HStack>
            <Text fontSize="14px" fontWeight="500" truncate>
              {item.subject || "(no subject)"}
            </Text>
            {outcome && (
              <Text
                fontSize="13px"
                color={item.error ? "st.failed" : "fg.mid"}
                mt={1}
                lineClamp={2}
              >
                {outcome}
              </Text>
            )}
          </Box>
          <Stack align="flex-end" gap={1} minW="fit-content">
            <Status status={item.status} />
            <Text fontSize="11px" color="fg.low">
              {when(item.completed_at ?? item.created_at)}
            </Text>
            {item.attempts > 1 && (
              <Text fontSize="10px" color="st.cancelled">
                {item.attempts} attempts
              </Text>
            )}
          </Stack>
        </Flex>
      </Panel>
    </RouterLink>
  );
}
