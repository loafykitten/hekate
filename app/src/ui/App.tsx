import type { QueryClient } from "@tanstack/react-query";
import { PersistQueryClientProvider } from "@tanstack/react-query-persist-client";
import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import type { Stage } from "../character/stage";
import { Conversation } from "../conversation/conversation";
import { applyApproval } from "../data/approvals";
import { createQueryClient } from "../data/hooks";
import { createQueryPersister, holdWhileChanging, PERSIST_BUSTER, PERSIST_MAX_AGE, shouldPersistQuery } from "../data/persist";
import { invalidationKeys } from "../data/queryKeys";
import { CardStack } from "./cards/CardStack";
import { ConnectionPill } from "./ConnectionPill";
import { ConversationProvider } from "./ConversationContext";
import { Dock } from "./Dock";
import { LogButton } from "./LogButton";
import { LogSheet } from "./LogSheet";
import { Menu } from "./menu/Menu";
import { isSection, type Section } from "./menu/sections";
import { MenuButton } from "./MenuButton";
import { StageView } from "./StageView";
import { StatusArea } from "./StatusArea";
import { Toasts } from "./Toasts";

// Dev-only tools; the dead branch is dropped from production builds.
const GesturePicker = import.meta.env.DEV ? lazy(() => import("./dev/GesturePicker")) : undefined;

// `?menu=reminders` (and the url in a push notification) opens the menu on that section.
function sectionFromUrl(url: string): Section | undefined {
  try {
    const value = new URL(url, location.href).searchParams.get("menu");
    return isSection(value) ? value : undefined;
  } catch {
    return undefined;
  }
}

// `?checkin=2026-10-20`: a tapped check-in notification, for that day.
function checkInFromUrl(url: string): string | undefined {
  try {
    return new URL(url, location.href).searchParams.get("checkin") ?? undefined;
  } catch {
    return undefined;
  }
}

export function App() {
  const [stage, setStage] = useState<Stage>();
  const [conversation] = useState(() => new Conversation());
  const [queryClient] = useState<QueryClient>(() => createQueryClient((message, key) => conversation.toast("error", message, key)));
  const [persister] = useState(() => holdWhileChanging(createQueryPersister(), () => queryClient.isMutating() > 0));
  const [logOpen, setLogOpen] = useState(false);
  const [menu, setMenu] = useState<{ open: boolean; section: Section }>(() => {
    const section = sectionFromUrl(location.href);
    return { open: section !== undefined, section: section ?? "today" };
  });
  const openLog = useCallback(() => {
    setMenu((m) => ({ ...m, open: false }));
    setLogOpen(true);
  }, []);
  const closeLog = useCallback(() => setLogOpen(false), []);
  const openMenu = useCallback((section?: Section) => {
    setLogOpen(false);
    setMenu((m) => ({ open: true, section: section ?? m.section }));
  }, []);
  const closeMenu = useCallback(() => setMenu((m) => ({ ...m, open: false })), []);
  const selectSection = useCallback((section: Section) => setMenu({ open: true, section }), []);

  useEffect(() => {
    conversation.start();
    // Don't wait out the backoff when the app comes back to the foreground or the network returns.
    const retry = () => {
      if (document.visibilityState === "visible") conversation.client.reconnectNow();
    };
    window.addEventListener("online", retry);
    document.addEventListener("visibilitychange", retry);
    return () => {
      window.removeEventListener("online", retry);
      document.removeEventListener("visibilitychange", retry);
      conversation.stop();
    };
  }, [conversation]);

  // Opened by the check-in notification: start it once (a reload doesn't start it again).
  useEffect(() => {
    const day = checkInFromUrl(location.href);
    if (day === undefined) return;
    conversation.startCheckIn(day);
    const url = new URL(location.href);
    url.searchParams.delete("checkin");
    history.replaceState(history.state, "", url);
  }, [conversation]);

  useEffect(() => {
    conversation.attachStage(stage);
    return () => conversation.attachStage(undefined);
  }, [conversation, stage]);

  // The service says what changed (and a fresh connection says everything might have): refetch
  // the menu data for those domains.
  useEffect(
    () =>
      conversation.onInvalidate((domains) => {
        for (const queryKey of invalidationKeys(domains)) void queryClient.invalidateQueries({ queryKey });
      }),
    [conversation, queryClient],
  );

  // An approval changed (here or on another device): every card and list showing it updates.
  useEffect(() => conversation.onApprovalChange((approval) => applyApproval(queryClient, approval)), [conversation, queryClient]);

  // A notification tapped while the app is open asks it to show the right section, or to start
  // the check-in.
  useEffect(() => {
    if (!("serviceWorker" in navigator)) return;
    const onMessage = (e: MessageEvent) => {
      const data = e.data as { type?: unknown; url?: unknown } | null;
      if (data?.type !== "open-url" || typeof data.url !== "string") return;
      const section = sectionFromUrl(data.url);
      if (section) openMenu(section);
      const day = checkInFromUrl(data.url);
      if (day !== undefined) {
        // She answers on the stage: nothing should cover it.
        closeMenu();
        closeLog();
        conversation.startCheckIn(day);
      }
    };
    navigator.serviceWorker.addEventListener("message", onMessage);
    return () => navigator.serviceWorker.removeEventListener("message", onMessage);
  }, [conversation, openMenu, closeMenu, closeLog]);

  return (
    <PersistQueryClientProvider
      client={queryClient}
      persistOptions={{
        persister,
        maxAge: PERSIST_MAX_AGE,
        buster: PERSIST_BUSTER,
        dehydrateOptions: { shouldDehydrateQuery: shouldPersistQuery },
      }}
    >
      <ConversationProvider value={conversation}>
        <StageView onStageChange={setStage} />
        <ConnectionPill />
        <LogButton onClick={openLog} />
        <MenuButton onClick={() => openMenu()} />
        <StatusArea />
        <CardStack hidden={menu.open || logOpen} onOpenSection={selectSection} />
        <Dock onOpenLog={openLog} logOpen={logOpen || menu.open} />
        <LogSheet open={logOpen} onClose={closeLog} />
        <Menu open={menu.open} section={menu.section} onSection={selectSection} onClose={closeMenu} />
        <Toasts onOpenSection={selectSection} />
        {GesturePicker && stage?.character && (
          <Suspense>
            <GesturePicker character={stage.character} />
          </Suspense>
        )}
      </ConversationProvider>
    </PersistQueryClientProvider>
  );
}
