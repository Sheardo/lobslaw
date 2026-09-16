import { Box, Button, Center, Field, HStack, Input, Stack, Text } from "@chakra-ui/react";
import { useCallback, useEffect, useState } from "react";
import { api, type Whoami } from "../api";
import { ErrorPanel, Loading, Panel } from "./ui";

/** Decides whether to show the console or a way in.
 *
 * It asks the node rather than inferring from a 401, because three
 * states need three screens: signed in, signed out with a way in, and
 * signed out with NO way in — the last being a deployment where
 * require_auth is on and no console token was configured. Rendering
 * that as "wrong password" would send somebody hunting for a password
 * that does not exist.
 */
export function LoginGate({ children }: { children: React.ReactNode }) {
  const [who, setWho] = useState<Whoami | null>(null);
  const [error, setError] = useState<Error | null>(null);

  const refresh = useCallback(() => {
    api.whoami().then(setWho).catch((err: Error) => setError(err));
  }, []);
  useEffect(refresh, [refresh]);

  if (error) {
    return <Center h="100vh" px={6}><Box maxW="440px" w="full"><ErrorPanel error={error} /></Box></Center>;
  }
  if (!who) return <Center h="100vh"><Loading /></Center>;
  if (who.authenticated) return <>{children}</>;
  return <SignIn who={who} onSignedIn={refresh} />;
}

function SignIn({ who, onSignedIn }: { who: Whoami; onSignedIn: () => void }) {
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error | null>(null);

  async function submit() {
    setBusy(true); setError(null);
    try { await api.login(token); setToken(""); onSignedIn(); }
    catch (err) { setError(err as Error); }
    finally { setBusy(false); }
  }

  return (
    <Center h="100vh" px={6}>
      <Panel p={7} maxW="400px" w="full">
        <HStack gap={2.5} mb={5}>
          <Box w="9px" h="9px" rounded="full" bg="brand.solid" />
          <Text fontWeight="600" letterSpacing="-0.02em">lobslaw</Text>
        </HStack>

        {who.login_available ? (
          <Stack gap={4}>
            <Field.Root>
              <Field.Label fontSize="13px">Console token</Field.Label>
              <Input
                type="password" value={token} autoFocus
                onChange={(e) => setToken(e.target.value)}
                onKeyDown={(e) => { if (e.key === "Enter") void submit(); }}
                bg="bg.s2" borderColor="edge.mid" rounded="control"
              />
              <Field.HelperText fontSize="xs" color="fg.low">
                The value behind [gateway.ui] token_ref.
              </Field.HelperText>
            </Field.Root>
            {error && <ErrorPanel error={error} />}
            <Button onClick={submit} loading={busy} disabled={!token}
              bg="brand.solid" color="#16100C" fontWeight="600" _hover={{ bg: "brand.hover" }} rounded="control">
              Sign in
            </Button>
          </Stack>
        ) : (
          /* No token configured. "Wrong password" here would send
             somebody hunting for one that does not exist. */
          <Stack gap={3}>
            <Text fontSize="sm" color="fg.mid">
              This node requires authentication but has no console token configured, so
              there is nothing to sign in with.
            </Text>
            <Box bg="bg.s2" borderWidth="1px" borderColor="edge.mid" rounded="control"
              p={3} fontFamily="mono" fontSize="12px" color="fg.mid">
              [gateway.ui]<br />
              token_ref = &quot;env:LOBSLAW_CONSOLE_TOKEN&quot;
            </Box>
            <Text fontSize="sm" color="fg.low">
              Set that and restart, or bind the gateway to 127.0.0.1 where the console
              needs no token at all.
            </Text>
          </Stack>
        )}
      </Panel>
    </Center>
  );
}
