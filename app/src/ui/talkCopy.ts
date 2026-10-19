import type { Phase } from "../conversation/conversation";

// What the hint under the talk button says, and the accessible name of the button itself.
export function talkCopy(
  phase: Phase,
  latched: boolean,
  connected: boolean,
  stt: boolean | undefined,
): { hint: string; label: string; disabled: boolean } {
  if (!connected) return { hint: "Offline", label: "Talk (offline)", disabled: true };
  if (stt === false) return { hint: "Voice input unavailable. You can type.", label: "Talk (unavailable)", disabled: true };
  switch (phase) {
    case "listening":
      return latched
        ? { hint: "Listening. Tap to send", label: "Stop listening and send", disabled: false }
        : { hint: "Release to send", label: "Listening", disabled: false };
    case "transcribing":
      return { hint: "Transcribing…", label: "Transcribing", disabled: true };
    case "thinking":
      return { hint: "Thinking…", label: "Talk (interrupts her)", disabled: false };
    case "speaking":
      return { hint: "Hold to interrupt", label: "Talk (interrupts her)", disabled: false };
    default:
      return { hint: "Hold to talk", label: "Talk", disabled: false };
  }
}
