// @vitest-environment jsdom
import { act, renderHook } from "@testing-library/react";
import { beforeEach, expect, it } from "vitest";
import { useTheme } from "./useTheme";

beforeEach(() => window.localStorage.clear());

it("synchronizes the theme preference across mounted consumers", () => {
  const { result } = renderHook(() => [useTheme(), useTheme()]);

  act(() => result.current[0].setPreference("dark"));

  expect(result.current[1].preference).toBe("dark");
  expect(result.current[1].resolved).toBe("dark");
});

it("synchronizes accent changes across mounted consumers", () => {
  const { result } = renderHook(() => [useTheme(), useTheme()]);

  act(() => result.current[0].setAccent("emerald"));

  expect(result.current[1].accent).toBe("emerald");
});
