import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { lazy, Suspense, useState } from "react";
import type { Stage } from "../character/stage";
import { ConnectionPill } from "./ConnectionPill";
import { StageView } from "./StageView";

// Dev-only tools; the dead branch is dropped from production builds.
const GesturePicker = import.meta.env.DEV ? lazy(() => import("./dev/GesturePicker")) : undefined;

const queryClient = new QueryClient();

export function App() {
  const [stage, setStage] = useState<Stage>();
  return (
    <QueryClientProvider client={queryClient}>
      <StageView onStageChange={setStage} />
      <ConnectionPill />
      {GesturePicker && stage?.character && (
        <Suspense>
          <GesturePicker character={stage.character} />
        </Suspense>
      )}
    </QueryClientProvider>
  );
}
