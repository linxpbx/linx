import { cleanup, render, screen } from "@testing-library/react";
import { App } from "./App";

// fetch stays stubbed after each test (each test stubs its own): a request
// the app starts as it unmounts must not reach the real network.
afterEach(() => cleanup());

// jsdom has no layout; the code boxes (input-otp) ask what's under a point to
// make room for a password manager's badge.
beforeAll(() => { document.elementFromPoint ??= () => null; });

test("shows sign-in when nobody is signed in", async () => {
  vi.stubGlobal("fetch", vi.fn(async () =>
    new Response(JSON.stringify({ type: "about:blank", title: "Unauthorized", status: 401, code: "unauthenticated", detail: "Sign in." }),
      { status: 401, headers: { "Content-Type": "application/problem+json" } })));
  render(<App />);
  expect(await screen.findByRole("heading", { name: "Sign in" })).toBeTruthy();
  expect(screen.getByLabelText("Email")).toBeTruthy();
  expect(screen.getByLabelText("Password")).toBeTruthy();
});

test("asks for the authenticator code when the session is waiting for it", async () => {
  vi.stubGlobal("fetch", vi.fn(async () =>
    new Response(JSON.stringify({ id: "u1", type: "user", scopes: [], pending: true, mfa_enabled: true }),
      { status: 200, headers: { "Content-Type": "application/json" } })));
  render(<App />);
  expect(await screen.findByRole("heading", { name: "Enter your code" })).toBeTruthy();
});
