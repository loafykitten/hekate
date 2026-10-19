import { keepPreviousData, MutationCache, QueryCache, QueryClient, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import type { CalendarEvent, Task } from "./model";
import { DOMAINS, keys, type Domain } from "./queryKeys";
import {
  ApiError,
  calendarApi,
  fetchWeather,
  memoryApi,
  remindersApi,
  tasksApi,
  type EventFields,
  type MemoryFields,
  type ReminderFields,
  type TaskFields,
} from "./rest";

// TanStack Query bindings for the menu. Queries fetch only while a section is on screen;
// mutations invalidate the domain they touch, and `invalidate` events from the socket do the same.

const STALE_MS = 20_000;

export function createQueryClient(onError: (message: string, key: string) => void): QueryClient {
  const describe = (e: unknown) => (e instanceof Error ? e.message : "Something went wrong.");
  return new QueryClient({
    queryCache: new QueryCache({
      onError: (error, query) => {
        // Only a menu section's first failure (nothing to show yet) is worth a toast. The health
        // poll and the log have their own ways of saying so.
        const section = String(query.queryKey[0]);
        if (query.state.data === undefined && (DOMAINS as readonly string[]).includes(section)) onError(describe(error), `rest-${String(query.queryKey[0])}`);
      },
    }),
    mutationCache: new MutationCache({
      onError: (error) => onError(describe(error), "rest-mutation"),
    }),
    defaultOptions: {
      queries: {
        staleTime: STALE_MS,
        retry: (count, error) => !(error instanceof ApiError && error.status >= 400 && error.status < 500) && count < 2,
        networkMode: "always",
      },
      mutations: { networkMode: "always" },
    },
  });
}

// A clock that re-renders its component, for relative times.
export function useNow(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

// --- tasks ---------------------------------------------------------------------------------

export function useTasks(list: string | undefined, status: "open" | "done" | "all", enabled = true) {
  return useQuery({
    queryKey: keys.tasks(list, status),
    queryFn: ({ signal }) => tasksApi.list({ list, status }, signal),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useTaskLists(enabled = true) {
  return useQuery({ queryKey: keys.lists, queryFn: ({ signal }) => tasksApi.lists(signal), enabled });
}

function useDomainMutation<V, D>(domains: Domain[], fn: (v: V) => Promise<D>) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSettled: () => Promise.all(domains.map((d) => qc.invalidateQueries({ queryKey: [d] }))),
  });
}

export function useCreateTask() {
  return useDomainMutation(["tasks"], (fields: TaskFields & { title: string }) => tasksApi.create(fields));
}

export function usePatchTask() {
  return useDomainMutation(["tasks"], ({ id, fields }: { id: string; fields: TaskFields }) => tasksApi.patch(id, fields));
}

export function useDeleteTask() {
  return useDomainMutation(["tasks"], (id: string) => tasksApi.remove(id));
}

type TaskCache = [readonly unknown[], Task[] | undefined][];

// Completing or reopening a task updates every cached task list at once, so the checkbox responds
// immediately; a failure puts the lists back.
export function useSetTaskDone() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, done }: { id: string; done: boolean }) => (done ? tasksApi.complete(id) : tasksApi.reopen(id)),
    onMutate: async ({ id, done }) => {
      await qc.cancelQueries({ queryKey: ["tasks", "list"] });
      const previous: TaskCache = qc.getQueriesData<Task[]>({ queryKey: ["tasks", "list"] });
      const stamp = new Date().toISOString();
      qc.setQueriesData<Task[]>({ queryKey: ["tasks", "list"] }, (tasks) => {
        if (!tasks) return tasks;
        return tasks.map((t) => {
          if (t.id !== id) return t;
          // A repeating task moves to its next date rather than closing; wait for the service.
          if (done && t.rrule) return t;
          return { ...t, completed_at: done ? stamp : null };
        });
      });
      return { previous };
    },
    onError: (_e, _v, ctx) => {
      for (const [key, data] of ctx?.previous ?? []) qc.setQueryData(key, data);
    },
    onSettled: () => qc.invalidateQueries({ queryKey: ["tasks"] }),
  });
}

// --- reminders -----------------------------------------------------------------------------

export function useReminders(status: "upcoming" | "past" | "all", enabled = true) {
  return useQuery({ queryKey: keys.reminders(status), queryFn: ({ signal }) => remindersApi.list(status, signal), enabled });
}

export function useCreateReminder() {
  return useDomainMutation(["reminders"], (f: ReminderFields & { text: string; at: string }) => remindersApi.create(f));
}

export function usePatchReminder() {
  return useDomainMutation(["reminders"], ({ id, fields }: { id: string; fields: ReminderFields }) => remindersApi.patch(id, fields));
}

export function useSnoozeReminder() {
  return useDomainMutation(["reminders"], ({ id, minutes }: { id: string; minutes: number }) => remindersApi.snooze(id, minutes));
}

export function useReminderDone() {
  return useDomainMutation(["reminders"], (id: string) => remindersApi.done(id));
}

export function useDeleteReminder() {
  return useDomainMutation(["reminders"], (id: string) => remindersApi.remove(id));
}

// --- calendar ------------------------------------------------------------------------------

export function useCalendarStatus(enabled = true) {
  return useQuery({ queryKey: keys.calendarStatus, queryFn: ({ signal }) => calendarApi.status(signal), enabled });
}

export function useEvents(from: string, to: string, enabled = true) {
  return useQuery<CalendarEvent[]>({
    queryKey: keys.events(from, to),
    queryFn: ({ signal }) => calendarApi.events(from, to, signal),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function useCreateEvent() {
  return useDomainMutation(["calendar"], (f: EventFields & { title: string; start: string }) => calendarApi.create(f));
}

export function usePatchEvent() {
  return useDomainMutation(["calendar"], ({ uid, fields }: { uid: string; fields: EventFields }) => calendarApi.patch(uid, fields));
}

export function useDeleteEvent() {
  return useDomainMutation(["calendar"], (uid: string) => calendarApi.remove(uid));
}

// --- memory --------------------------------------------------------------------------------

export function useMemories(q: string, kind: string, enabled = true) {
  return useQuery({
    queryKey: keys.memories(q, kind),
    queryFn: ({ signal }) => memoryApi.list({ q: q || undefined, kind: kind || undefined, limit: 100 }, signal),
    placeholderData: keepPreviousData,
    enabled,
  });
}

export function usePatchMemory() {
  return useDomainMutation(["memory"], ({ id, fields }: { id: string; fields: MemoryFields }) => memoryApi.patch(id, fields));
}

export function useDeleteMemory() {
  return useDomainMutation(["memory"], (id: string) => memoryApi.remove(id));
}

export function useCoreMemory(enabled = true) {
  return useQuery({ queryKey: keys.core, queryFn: ({ signal }) => memoryApi.core(signal), enabled });
}

export function usePutCore() {
  return useDomainMutation(["memory"], ({ key, value }: { key: string; value: string }) => memoryApi.putCore(key, value));
}

export function useDeleteCore() {
  return useDomainMutation(["memory"], (key: string) => memoryApi.removeCore(key));
}

// --- weather -------------------------------------------------------------------------------

// null data means no home place is set yet.
export function useWeather(enabled = true) {
  return useQuery({
    queryKey: keys.weather(undefined),
    queryFn: ({ signal }) => fetchWeather(undefined, signal),
    staleTime: 10 * 60_000,
    enabled,
  });
}
