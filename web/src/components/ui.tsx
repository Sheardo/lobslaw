import { Box, Center, HStack, Spinner, Text } from "@chakra-ui/react";
import { useCallback, useEffect, useState } from "react";
import { ApiError, type BotStatus } from "../api";
import { botColors, initials } from "../theme";

/** useLoad wraps the three states every screen has: loading, an error
 * worth showing, and data.
 *
 * A hook rather than a per-screen effect because getting the third
 * state wrong produces a console that renders an empty list when the
 * node is unreachable — and "no bots yet" and "the API is down" need
 * different responses from the person reading it. */
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
      .catch((err: Error) => live && setError(err))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);

  useEffect(() => reload(), [reload]);
  return { data, error, loading, reload };
}

/** Avatar is a bot's identity, everywhere it appears.
 *
 * Colour comes from the id, so the same bot is the same colour in the
 * sidebar, on its queue items and against its chat replies. That
 * consistency is what turns a list you read into a team you
 * recognise. */
export function Avatar({
  id,
  name,
  size = 32,
  dimmed,
}: {
  id: string;
  name?: string;
  size?: number;
  dimmed?: boolean;
}) {
  const c = botColors(id);
  return (
    <Center
      w={`${size}px`}
      h={`${size}px`}
      minW={`${size}px`}
      rounded="full"
      bg={c.muted}
      borderWidth="1px"
      borderColor={c.border}
      color={c.text}
      fontSize={`${Math.round(size * 0.38)}px`}
      fontWeight="600"
      letterSpacing="tight"
      opacity={dimmed ? 0.45 : 1}
      transition="opacity 120ms"
      userSelect="none"
    >
      {initials(name || id)}
    </Center>
  );
}

const STATUS_LABEL: Record<BotStatus, string> = {
  pending: "Queued",
  claimed: "Working",
  done: "Done",
  failed: "Failed",
  cancelled: "Cancelled",
};

/** Status as a dot plus a word.
 *
 * A coloured dot is scannable down a column in a way a pill is not,
 * and the word beside it means the colour never has to carry the
 * meaning on its own — which matters for the eight percent of people
 * for whom red and green are the same dot. */
export function Status({ status }: { status: BotStatus }) {
  const live = status === "claimed";
  return (
    <HStack gap={2} minW="fit-content">
      <Box
        w="7px"
        h="7px"
        rounded="full"
        bg={`st.${status}`}
        
        css={
          live
            ? {
                animation: "pulse 1.6s ease-in-out infinite",
                "@keyframes pulse": {
                  "0%, 100%": { boxShadow: "0 0 0 0 rgba(91,157,240,0.5)" },
                  "50%": { boxShadow: "0 0 0 4px rgba(91,157,240,0)" },
                },
              }
            : undefined
        }
      />
      <Text fontSize="xs" color="fg.mid" fontWeight="500">
        {STATUS_LABEL[status] ?? status}
      </Text>
    </HStack>
  );
}

export function Panel({
  children,
  ...rest
}: { children: React.ReactNode } & Record<string, unknown>) {
  return (
    <Box
      bg="bg.s1"
      borderWidth="1px"
      borderColor="edge.soft"
      rounded="card"
      {...rest}
    >
      {children}
    </Box>
  );
}

export function Loading() {
  return (
    <Center py={16}>
      <Spinner size="md" color="fg.low" borderWidth="2px" />
    </Center>
  );
}

/** ErrorPanel says what went wrong in the API's own words.
 *
 * Deliberately not a generic apology: the node's messages are written
 * for a person — "engineering has 200 pending items (cap 200); it is
 * not keeping up" — and replacing them with something friendlier
 * throws away the only useful part. */
export function ErrorPanel({ error }: { error: Error }) {
  const status = error instanceof ApiError ? error.status : undefined;
  const hint =
    status === 401
      ? "Sign in, or reach this node over loopback."
      : status === 503
        ? "This node does not host the bot registry. Try one running the memory function."
        : undefined;

  return (
    <Box
      bg="bg.s1"
      borderWidth="1px"
      borderLeftWidth="3px"
      borderColor="edge.soft"
      borderLeftColor="st.failed"
      rounded="card"
      px={4}
      py={3}
    >
      <Text fontSize="xs" color="st.failed" fontWeight="600" mb={1}>
        {status ? `Error ${status}` : "Error"}
      </Text>
      <Text fontSize="sm" fontFamily="mono" color="fg.hi" lineHeight="1.6">
        {error.message}
      </Text>
      {hint && (
        <Text mt={2} fontSize="sm" color="fg.mid">
          {hint}
        </Text>
      )}
    </Box>
  );
}

export function Empty({ title, hint }: { title: string; hint?: string }) {
  return (
    <Center py={16} flexDirection="column" textAlign="center">
      <Text fontWeight="500" color="fg.mid">
        {title}
      </Text>
      {hint && (
        <Text mt={1} fontSize="sm" color="fg.low" maxW="sm">
          {hint}
        </Text>
      )}
    </Center>
  );
}

/** Relative time, because "4 minutes ago" is the question somebody is
 * actually asking of a queue. Falls back to a date once that stops
 * being useful. */
export function when(ts?: string): string {
  if (!ts) return "";
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return ts;
  const secs = Math.floor((Date.now() - d.getTime()) / 1000);
  if (secs < 10) return "just now";
  if (secs < 60) return `${secs}s ago`;
  if (secs < 3600) return `${Math.floor(secs / 60)}m ago`;
  if (secs < 86400) return `${Math.floor(secs / 3600)}h ago`;
  if (secs < 604800) return `${Math.floor(secs / 86400)}d ago`;
  return d.toLocaleDateString();
}
