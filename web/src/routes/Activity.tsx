import {
  Badge,
  Box,
  Button,
  Flex,
  HStack,
  Heading,
  Table,
  Text,
} from "@chakra-ui/react";
import { Link as RouterLink } from "react-router-dom";
import { api, type InboxItem } from "../api";
import {
  EmptyState,
  ErrorPanel,
  Loading,
  StatusBadge,
  useLoad,
  when,
} from "../components/common";

/** The cross-bot timeline.
 *
 * Built from the inboxes rather than from sessions, because the
 * question this page answers is "what is the team doing" — and a
 * session index answers "what conversations exist", which stops being
 * the same thing the moment bots start working items nobody chatted
 * about.
 */
export function Activity() {
  const { data, error, loading, reload } = useLoad(() => api.activity(100));

  return (
    <Box>
      <Flex align="center" justify="space-between" mb={5}>
        <Box>
          <Heading size="lg">Activity</Heading>
          <Text color="fg.muted" fontSize="sm">
            Every bot's queue, newest first.
          </Text>
        </Box>
        <Button size="sm" variant="outline" onClick={reload} loading={loading}>
          Refresh
        </Button>
      </Flex>

      {error && <ErrorPanel error={error} />}
      {!error && loading && !data && <Loading />}
      {!error && data && data.length === 0 && (
        <EmptyState
          title="Nothing has happened yet"
          hint="Assign a bot some work from its page, or ask the chief of staff to."
        />
      )}
      {!error && data && data.length > 0 && <ActivityTable items={data} />}
    </Box>
  );
}

function ActivityTable({ items }: { items: InboxItem[] }) {
  return (
    <Table.Root size="sm" variant="line" interactive>
      <Table.Header>
        <Table.Row>
          <Table.ColumnHeader>Bot</Table.ColumnHeader>
          <Table.ColumnHeader>Subject</Table.ColumnHeader>
          <Table.ColumnHeader>From</Table.ColumnHeader>
          <Table.ColumnHeader>Kind</Table.ColumnHeader>
          <Table.ColumnHeader>Status</Table.ColumnHeader>
          <Table.ColumnHeader>When</Table.ColumnHeader>
        </Table.Row>
      </Table.Header>
      <Table.Body>
        {items.map((item) => (
          <Table.Row key={`${item.recipient}/${item.id}`}>
            <Table.Cell>
              <RouterLink to={`/bots/${item.recipient}`}>
                <Text fontWeight="medium" textDecoration="underline">
                  {item.recipient}
                </Text>
              </RouterLink>
            </Table.Cell>
            <Table.Cell maxW="sm" truncate>
              {item.subject || <Text color="fg.muted">(no subject)</Text>}
            </Table.Cell>
            <Table.Cell>
              <Text fontSize="sm" color="fg.muted">
                {item.sender}
              </Text>
            </Table.Cell>
            <Table.Cell>
              <Badge variant="outline">{item.kind}</Badge>
            </Table.Cell>
            <Table.Cell>
              <HStack gap={2}>
                <StatusBadge status={item.status} />
                {/* Attempts are shown only when there have been
                    several. A "1" beside every row is noise; a "3"
                    beside one is the thing you were looking for. */}
                {item.attempts > 1 && (
                  <Text fontSize="xs" color="fg.muted">
                    {item.attempts} attempts
                  </Text>
                )}
              </HStack>
            </Table.Cell>
            <Table.Cell whiteSpace="nowrap">
              <Text fontSize="sm" color="fg.muted">
                {when(item.completed_at ?? item.created_at)}
              </Text>
            </Table.Cell>
          </Table.Row>
        ))}
      </Table.Body>
    </Table.Root>
  );
}
