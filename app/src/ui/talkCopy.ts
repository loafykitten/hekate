import type { Phase } from "../conversation/conversation";

// "Click" with a mouse, "Tap" on a touch screen.
export type TalkVerb = "Click" | "Tap";

export function talkVerb(): TalkVerb {
  return typeof window !== "undefined" && window.matchMedia?.("(hover: hover) and (pointer: fine)").matches ? "Click" : "Tap";
}

// What the hint under the talk button says, and the accessible name of the button itself.
// Talking is a toggle: one click or tap starts it, the next ends it and sends.
export function talkCopy(
  phase: Phase,
  connected: boolean,
  stt: boolean | undefined,
  verb: TalkVerb,
): { hint: string; label: string; disabled: boolean } {
  if (!connected) return { hint: "Offline", label: "Talk (offline)", disabled: true };
  if (stt === false) return { hint: "Voice input unavailable. You can type.", label: "Talk (unavailable)", disabled: true };
  switch (phase) {
    case "starting":
      return { hint: "Opening the mic…", label: "Opening the microphone", disabled: false };
    case "listening":
      return { hint: `${verb} again to end`, label: "Stop listening and send", disabled: false };
    case "transcribing":
      return { hint: "Transcribing…", label: "Transcribing", disabled: true };
    case "thinking":
      return { hint: "Thinking…", label: "Talk (interrupts her)", disabled: false };
    case "speaking":
      return { hint: `${verb} to interrupt`, label: "Talk (interrupts her)", disabled: false };
    default:
      return { hint: `${verb} to talk`, label: "Talk", disabled: false };
  }
}
