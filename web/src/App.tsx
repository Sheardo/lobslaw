import { Box, Center, Flex, HStack, Stack, Text } from "@chakra-ui/react";
import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { api } from "./api";
import { LoginGate } from "./components/LoginGate";
import { Avatar, useLoad } from "./components/ui";
import { Activity } from "./routes/Activity";
import { BotDetail } from "./routes/BotDetail";
import { Chat } from "./routes/Chat";
import { Config } from "./routes/Config";
import { NewBot } from "./routes/NewBot";

export function App() {
  return (
    <LoginGate>
      <Shell />
    </LoginGate>
  );
}

/** The roster is the frame, not a page.
 *
 * A console about a TEAM should show the team at all times. The first
 * version had the bots behind a nav link, which made "who works here"
 * a click away and every other screen anonymous — and it is the reason
 * the whole thing read as a CRUD admin panel rather than something
 * with people in it.
 */
function Shell() {
  const { data: bots, reload } = useLoad(() => api.listBots());
  const location = useLocation();

  return (
    <Flex h="100vh" overflow="hidden">
      <Box
        as="aside"
        w="248px"
        minW="248px"
        bg="bg.s1"
        borderRightWidth="1px"
        borderColor="edge.soft"
        display="flex"
        flexDirection="column"
      >
        <HStack px={5} py={4} gap={2.5}>
          <Box w="9px" h="9px" rounded="full" bg="brand.solid" />
          <Text fontWeight="600" letterSpacing="-0.02em" fontSize="15px">
            lobslaw
          </Text>
        </HStack>

        <Stack px={3} gap={0.5}>
          <Item to="/activity" label="Activity" />
          <Item to="/chat" label="Chat" />
          <Item to="/config" label="Config" />
        </Stack>

        <Text
          px={5}
          pt={6}
          pb={2}
          fontSize="10px"
          fontWeight="600"
          letterSpacing="0.08em"
          color="fg.low"
          textTransform="uppercase"
        >
          Team
        </Text>

        <Stack px={3} gap={0.5} flex="1" overflowY="auto" pb={3}>
          {(bots ?? []).map((bot) => {
            const active = location.pathname === `/bots/${bot.id}`;
            return (
              <NavLink key={bot.id} to={`/bots/${bot.id}`}>
                <HStack
                  px={2}
                  py={2}
                  gap={2.5}
                  rounded="control"
                  bg={active ? "bg.s3" : undefined}
                  _hover={{ bg: active ? "bg.s3" : "bg.s2" }}
                  transition="background 120ms"
                >
                  <Avatar id={bot.id} name={bot.display_name} size={26} dimmed={!bot.enabled} />
                  <Box minW={0} flex="1">
                    <Text
                      fontSize="13px"
                      fontWeight={active ? "600" : "500"}
                      color={bot.enabled ? "fg.hi" : "fg.low"}
                      truncate
                    >
                      {bot.display_name || bot.id}
                    </Text>
                  </Box>
                  {bot.is_coordinator && (
                    <Box w="5px" h="5px" rounded="full" bg="brand.solid" title="coordinator" />
                  )}
                </HStack>
              </NavLink>
            );
          })}

          <NavLink to="/bots/new">
            <HStack
              px={2}
              py={2}
              gap={2.5}
              rounded="control"
              _hover={{ bg: "bg.s2" }}
              transition="background 120ms"
            >
              <Center
                w="26px"
                h="26px"
                minW="26px"
                rounded="full"
                borderWidth="1px"
                borderStyle="dashed"
                borderColor="edge.hard"
                color="fg.low"
                fontSize="14px"
              >
                +
              </Center>
              <Text fontSize="13px" color="fg.low">
                New bot
              </Text>
            </HStack>
          </NavLink>
        </Stack>
      </Box>

      <Box as="main" flex="1" overflowY="auto" bg="bg.canvas">
        <Routes>
          {/* Activity first. The question somebody opens this to answer
              is "what is the team doing"; a roster answers "who exists",
              and the sidebar already does that permanently. */}
          <Route path="/" element={<Navigate to="/activity" replace />} />
          <Route path="/activity" element={<Activity />} />
          <Route path="/chat" element={<Chat />} />
          <Route path="/config" element={<Config />} />
          <Route path="/bots/new" element={<NewBot onCreated={reload} />} />
          <Route path="/bots/:botId" element={<BotDetail onChanged={reload} />} />
          <Route path="*" element={<NotFound />} />
        </Routes>
      </Box>
    </Flex>
  );
}

function Item({ to, label }: { to: string; label: string }) {
  return (
    <NavLink to={to}>
      {({ isActive }) => (
        <Box
          px={2.5}
          py={1.5}
          rounded="control"
          fontSize="13px"
          fontWeight={isActive ? "600" : "500"}
          color={isActive ? "fg.hi" : "fg.mid"}
          bg={isActive ? "bg.s3" : undefined}
          _hover={{ bg: isActive ? "bg.s3" : "bg.s2", color: "fg.hi" }}
          transition="all 120ms"
        >
          {label}
        </Box>
      )}
    </NavLink>
  );
}

function NotFound() {
  return (
    <Center h="full" flexDirection="column">
      <Text fontWeight="600" fontSize="lg">
        Nothing here
      </Text>
      <Text color="fg.mid" fontSize="sm" mt={1}>
        That page does not exist.
      </Text>
    </Center>
  );
}

/** Page is the common frame: a title, an optional action, and a body
 * that scrolls. Repeated per-screen padding is how a console drifts
 * into looking like several applications. */
export function Page({
  title,
  subtitle,
  action,
  children,
}: {
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <Box maxW="1100px" mx="auto" px={8} py={7}>
      <Flex align="flex-start" justify="space-between" gap={6} mb={6}>
        <Box minW={0}>
          <Box fontSize="22px" fontWeight="600" letterSpacing="-0.02em">
            {title}
          </Box>
          {subtitle && (
            <Text color="fg.mid" fontSize="sm" mt={1}>
              {subtitle}
            </Text>
          )}
        </Box>
        {action}
      </Flex>
      {children}
    </Box>
  );
}
