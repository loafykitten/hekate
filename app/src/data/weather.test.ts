import { describe, expect, it } from "vitest";
import { degrees, isDaytime, precipLabel, weatherGroup, type WeatherGroup } from "./weather";

describe("weatherGroup", () => {
  const cases: [number, WeatherGroup][] = [
    [0, "clear"],
    [1, "clear"],
    [2, "partly-cloudy"],
    [3, "cloudy"],
    [45, "fog"],
    [48, "fog"],
    [51, "drizzle"],
    [55, "drizzle"],
    [56, "drizzle"],
    [57, "drizzle"],
    [61, "rain"],
    [65, "rain"],
    [66, "rain"],
    [67, "rain"],
    [80, "rain"],
    [82, "rain"],
    [71, "snow"],
    [75, "snow"],
    [77, "snow"],
    [85, "snow"],
    [86, "snow"],
    [95, "thunder"],
    [96, "thunder"],
    [99, "thunder"],
  ];
  it.each(cases)("maps WMO code %i to %s", (code, group) => {
    expect(weatherGroup(code)).toBe(group);
  });

  it("falls back to cloudy for codes it doesn't know", () => {
    expect(weatherGroup(42)).toBe("cloudy");
    expect(weatherGroup(-1)).toBe("cloudy");
    expect(weatherGroup(Number.NaN)).toBe("cloudy");
  });
});

describe("isDaytime", () => {
  const z = { tz: "America/New_York" };
  const sunrise = "2026-10-20T07:10:00-04:00";
  const sunset = "2026-10-20T18:15:00-04:00";
  it("is day between sunrise and sunset, by time of day", () => {
    expect(isDaytime("2026-10-20T12:00:00-04:00", sunrise, sunset, z)).toBe(true);
    expect(isDaytime("2026-10-20T06:00:00-04:00", sunrise, sunset, z)).toBe(false);
    expect(isDaytime("2026-10-20T19:00:00-04:00", sunrise, sunset, z)).toBe(false);
    // Tomorrow's hours use today's sunrise and sunset times.
    expect(isDaytime("2026-10-21T08:00:00-04:00", sunrise, sunset, z)).toBe(true);
    expect(isDaytime("2026-10-21T23:00:00-04:00", sunrise, sunset, z)).toBe(false);
  });

  it("assumes day when sunrise or sunset is unknown", () => {
    expect(isDaytime("2026-10-20T03:00:00-04:00", "", "", z)).toBe(true);
  });
});

describe("degrees", () => {
  it("rounds and adds the degree sign", () => {
    expect(degrees(71.6)).toBe("72°");
    expect(degrees(-0.4)).toBe("0°");
    expect(degrees(-3.6)).toBe("-4°");
  });
});

describe("precipLabel", () => {
  it("shows a percentage, or nothing without data or below the minimum", () => {
    expect(precipLabel(60)).toBe("60%");
    expect(precipLabel(0)).toBe("0%");
    expect(precipLabel(null)).toBe("");
    expect(precipLabel(undefined)).toBe("");
    expect(precipLabel(10, 20)).toBe("");
    expect(precipLabel(20, 20)).toBe("20%");
  });
});
