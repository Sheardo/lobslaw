import {
  Box,
  Button,
  Card,
  Center,
  Field,
  Heading,
  Input,
  Stack,
  Text,
} from "@chakra-ui/react";
import { useCallback, useEffect, useState } from "react";
import { api, type Whoami } from "../api";
import { ErrorPanel, Loading } from "./common";

/** LoginGate decides whether to show the console or a login form.
 *
 * It asks the node rather than guessing from a 401, because the three
 * states need different screens: signed in, signed out with a way in,
 * and signed out with NO way in — the last being a deployment where
 * require_auth is on and no console token was configured. Treating
 * that as "wrong password" would send somebody hunting for a password
 * that does not exist.
 */
export function LoginGate({ children }: { children: React.ReactNode }) {
  const [who, setWho] = useState<Whoami | null>(null);
  const [error, setError] = useState<Error | null>(null);

  const refresh = useCallback(() => {
    api
      .whoami()
      .then(setWho)
      .catch((err: Error) => setError(err));
  }, []);

  useEffect(refresh, [refresh]);

  if (error) {
    return (
      <Center py={16}>
        <Box maxW="lg" w="full">
          <ErrorPanel error={error} />
        </Box>
      </Center>
    );
  }
  if (!who) return <Loading />;
  if (who.authenticated) {
    return <>{children}</>;
  }
  return <LoginForm who={who} onSignedIn={refresh} />;
}

function LoginForm({ who, onSignedIn }: { who: Whoami; onSignedIn: () => void }) {
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<Error | null>(null);

  async function submit() {
    setBusy(true);
    setError(null);
    try {
      await api.login(token);
      setToken("");
      onSignedIn();
    } catch (err) {
      setError(err as Error);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Center py={16}>
      <Card.Root variant="outline" maxW="md" w="full">
        <Card.Body>
          <Heading size="md" mb={1}>
            Sign in
          </Heading>

          {who.login_available ? (
            <Stack gap={4} mt={4}>
              <Text fontSize="sm" color="fg.muted">
                Enter the token from <code>[gateway.ui] token_ref</code>.
              </Text>
              <Field.Root>
                <Field.Label>Console token</Field.Label>
                <Input
                  type="password"
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") void submit();
                  }}
                  autoFocus
                />
              </Field.Root>
              {error && <ErrorPanel error={error} />}
              <Button onClick={submit} loading={busy} disabled={!token}>
                Sign in
              </Button>
            </Stack>
          ) : (
            /* No token configured. Saying "wrong password" here would
               send somebody hunting for one that does not exist. */
            <Stack gap={3} mt={4}>
              <Text fontSize="sm">
                This node requires authentication but has no console token
                configured, so there is nothing to sign in with.
              </Text>
              <Box borderWidth="1px" rounded="md" p={3} fontFamily="mono" fontSize="xs">
                [gateway.ui]
                <br />
                token_ref = "env:LOBSLAW_CONSOLE_TOKEN"
              </Box>
              <Text fontSize="sm" color="fg.muted">
                Set that and restart the node. Or bind the gateway to 127.0.0.1, where
                the console needs no token at all.
              </Text>
            </Stack>
          )}
        </Card.Body>
      </Card.Root>
    </Center>
  );
}
