import { createContext, useContext, useSyncExternalStore } from "react";
import type { Conversation, ConversationState } from "../conversation/conversation";
import type { ThreadLive } from "../conversation/threadLive";

const Context = createContext<Conversation | undefined>(undefined);

export const ConversationProvider = Context.Provider;

export function useConversation(): Conversation {
  const conversation = useContext(Context);
  if (!conversation) throw new Error("useConversation needs a ConversationProvider");
  return conversation;
}

export function useConversationState(): ConversationState {
  const conversation = useConversation();
  return useSyncExternalStore(conversation.subscribe, conversation.getState);
}

// A thread's turn as it happens (see conversation/threadLive).
export function useThreadLive(thread: string): ThreadLive {
  const { threads } = useConversation();
  return useSyncExternalStore(threads.subscribe, () => threads.get(thread));
}
