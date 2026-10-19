import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { lazy, Suspense, useCallback, useEffect, useState } from "react";
import type { Stage } from "../character/stage";
import { Conversation } from "../conversation/conversation";
import { ConnectionPill } from "./ConnectionPill";
import { ConversationProvider } from "./ConversationContext";
import { Dock } from "./Dock";
import { LogButton } from "./LogButton";
import { LogSheet } from "./LogSheet";
import { StageView } from "./StageView";
import { StatusArea } from "./StatusArea";
import { Toasts } from "./Toasts";

// Dev-only tools; the dead branch is dropped from production builds.
const GesturePicker = import.meta.env.DEV ? lazy(() => import("./dev/GesturePicker")) : undefined;

const queryClient = new QueryClient();

export function App() {
  const [stage, setStage] = useState<Stage>();
  const [conversation] = useState(() => new Conversation());
  const [logOpen, setLogOpen] = useState(false);
  const openLog = useCallback(() => setLogOpen(true), []);
  const closeLog = useCallback(() => setLogOpen(false), []);

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

  useEffect(() => {
    conversation.attachStage(stage);
    return () => conversation.attachStage(undefined);
  }, [conversation, stage]);

  return (
    <QueryClientProvider client={queryClient}>
      <ConversationProvider value={conversation}>
        <StageView onStageChange={setStage} />
        <ConnectionPill />
        <LogButton onClick={openLog} />
        <StatusArea />
        <Dock onOpenLog={openLog} logOpen={logOpen} />
        <LogSheet open={logOpen} onClose={closeLog} />
        <Toasts />
        {GesturePicker && stage?.character && (
          <Suspense>
            <GesturePicker character={stage.character} />
          </Suspense>
        )}
      </ConversationProvider>
    </QueryClientProvider>
  );
}
