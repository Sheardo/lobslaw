import { Badge, Box, Center, Heading, Spinner, Text } from "@chakra-ui/react";
import { useCallback, useEffect, useState } from "react";
import { ApiError, statusPalette, type BotStatus } from "../api";

/** useLoad wraps the three states every screen here has: loading, an
 * error worth showing, and data.
 *
 * A hook rather than a per-screen useEffect because getting the third
 * state wrong is what produces a console that renders an empty list
 * when the node is actually unreachable — and "no bots yet" and "the
 * API is down" need different responses from the person reading it. */
export function useLoad<T>(fn: () => Promise<T>, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<Error | null>(null);
  const [loading, setLoading] = useState(true);

  const reload = useCallback(() => {
    let live = true;
    setLoading(true);
    fn()
      .then((value) => {
        if (live) {
          setData(value);
          setError(null);
        }
      })
      .catch((err: Error) => {
        if (live) setError(err);
      })
      .finally(() => {
        if (live) setLoading(false);
      });
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  useEffect(() => reload(), [reload]);

  return { data, error, loading, reload };
}

export function Loading() {
  return (
    <Center py={12}>
      <Spinner size="lg" />
    </Center>
  );
}

/** ErrorPanel says what went wrong in the API's own words.
 *
 * Deliberately not a generic "something went wrong": the messages the
 * node sends are written for a person — "engineering has 200 pending
 * items (cap 200); it is not keeping up" — and replacing them with a
 * friendly nothing throws away the only useful part. */
export function ErrorPanel({ error }: { error: Error }) {
  const status = error instanceof ApiError ? error.status : undefined;
  const hint =
    status === 401
      ? "This node requires a token. Sign in, or reach it over loopback."
      : status === 503
        ? "This node does not host the bot registry. Try one that runs the memory function."
        : undefined;

  return (
    <Box borderWidth="1px" borderColor="red.400" rounded="md" p={4}>
      <Heading size="sm" color="red.500" mb={1}>
        {status ? `Error ${status}` : "Error"}
      </Heading>
      <Text fontFamily="mono" fontSize="sm">
        {error.message}
      </Text>
      {hint && (
        <Text mt={2} fontSize="sm" color="fg.muted">
          {hint}
        </Text>
      )}
    </Box>
  );
}

export function EmptyState({ title, hint }: { title: string; hint?: string }) {
  return (
    <Box borderWidth="1px" borderStyle="dashed" rounded="md" p={8} textAlign="center">
      <Text fontWeight="medium">{title}</Text>
      {hint && (
        <Text mt={1} fontSize="sm" color="fg.muted">
          {hint}
        </Text>
      )}
    </Box>
  );
}

export function StatusBadge({ status }: { status: BotStatus }) {
  return (
    <Badge colorPalette={statusPalette[status] ?? "gray"} variant="subtle">
      {status}
    </Badge>
  );
}

/** Timestamps render in the reader's own zone. The API sends UTC; a
 * queue that says 03:00 when the person remembers 04:00 is a queue
 * they stop trusting. */
export function when(ts?: string) {
  if (!ts) return "";
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return ts;
  return d.toLocaleString();
}
