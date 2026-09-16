import { Box, Flex, HStack, Heading, Link, Text } from "@chakra-ui/react";
import { NavLink, Navigate, Route, Routes } from "react-router-dom";
import { Activity } from "./routes/Activity";
import { BotDetail } from "./routes/BotDetail";
import { Bots } from "./routes/Bots";
import { Chat } from "./routes/Chat";

const NAV = [
  { to: "/activity", label: "Activity" },
  { to: "/bots", label: "Bots" },
  { to: "/chat", label: "Chat" },
];

export function App() {
  return (
    <Flex direction="column" minH="100vh">
      <Box as="header" borderBottomWidth="1px" px={6} py={3}>
        <Flex align="center" gap={8}>
          <Heading size="md" letterSpacing="tight">
            lobslaw
          </Heading>
          <HStack gap={1}>
            {NAV.map((item) => (
              <NavLink key={item.to} to={item.to}>
                {({ isActive }) => (
                  <Link
                    asChild
                    px={3}
                    py={1.5}
                    rounded="md"
                    fontWeight={isActive ? "semibold" : "normal"}
                    bg={isActive ? "bg.emphasized" : undefined}
                    _hover={{ textDecoration: "none", bg: "bg.subtle" }}
                  >
                    <span>{item.label}</span>
                  </Link>
                )}
              </NavLink>
            ))}
          </HStack>
        </Flex>
      </Box>

      <Box as="main" flex="1" px={6} py={6} maxW="6xl" w="full" mx="auto">
        <Routes>
          {/* Activity is the front page. The question somebody opens
              this to answer is "what is the team doing", and a bot
              list answers "who exists" — which they usually already
              know. */}
          <Route path="/" element={<Navigate to="/activity" replace />} />
          <Route path="/activity" element={<Activity />} />
          <Route path="/bots" element={<Bots />} />
          <Route path="/bots/:botId" element={<BotDetail />} />
          <Route path="/chat" element={<Chat />} />
          <Route path="*" element={<NotFound />} />
        </Routes>
      </Box>
    </Flex>
  );
}

function NotFound() {
  return (
    <Box py={12} textAlign="center">
      <Heading size="lg" mb={2}>
        Nothing here
      </Heading>
      <Text color="fg.muted">That page does not exist.</Text>
    </Box>
  );
}
