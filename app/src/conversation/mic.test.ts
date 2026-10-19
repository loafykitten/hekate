import { describe, expect, it } from "vitest";
import { classifyMicError, MicError } from "./mic";

const named = (name: string) => Object.assign(new Error("x"), { name });

describe("classifyMicError", () => {
  it("separates a blocked mic from a missing or busy one", () => {
    expect(classifyMicError(named("NotAllowedError")).kind).toBe("denied");
    expect(classifyMicError(named("NotFoundError")).kind).toBe("no-device");
    expect(classifyMicError(named("NotReadableError")).kind).toBe("busy");
    expect(classifyMicError(new Error("weird")).kind).toBe("failed");
    expect(classifyMicError("string").kind).toBe("failed");
  });

  it("words each kind for the user and passes MicErrors through", () => {
    expect(classifyMicError(named("NotAllowedError")).message).toMatch(/blocked/);
    expect(classifyMicError(named("NotFoundError")).message).toMatch(/No microphone/);
    const own = new MicError("insecure", "needs https");
    expect(classifyMicError(own)).toBe(own);
  });
});
