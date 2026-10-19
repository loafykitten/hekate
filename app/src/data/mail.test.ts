import { beforeEach, describe, expect, it } from "vitest";
import { categoryLabel, filterCategories, mailWhen, senderName, totalUnread } from "./mail";
import { setZone } from "./time";

beforeEach(() => setZone({ tz: "UTC", locale: "en-US" }));

const now = Date.parse("2026-10-19T15:00:00Z");

describe("mail formatting", () => {
  it("shows the time today, then Yesterday, then the date", () => {
    expect(mailWhen("2026-10-19T08:12:00Z", now)).toBe("8:12 AM");
    expect(mailWhen("2026-10-18T23:00:00Z", now)).toBe("Yesterday");
    expect(mailWhen("2026-10-12T08:00:00Z", now)).toBe("Oct 12");
    expect(mailWhen("2025-03-02T08:00:00Z", now)).toBe("Mar 2, 2025");
    expect(mailWhen("garbage", now)).toBe("");
  });

  it("names the sender by name, then address", () => {
    expect(senderName({ name: "Shopify", address: "no-reply@example.com" })).toBe("Shopify");
    expect(senderName({ name: "", address: "no-reply@example.com" })).toBe("no-reply@example.com");
    expect(senderName({ name: "", address: "" })).toBe("Unknown sender");
  });

  it("labels categories and keeps their order", () => {
    expect(categoryLabel("needs_reply")).toBe("Needs reply");
    expect(categoryLabel("newsletter")).toBe("Newsletter");
    expect(categoryLabel(null)).toBe("");
    expect(filterCategories({ promo: 2, odd: 1 }).slice(-2)).toEqual(["promo", "odd"]);
    expect(totalUnread({ promo: 2, newsletter: 3 })).toBe(5);
  });
});
