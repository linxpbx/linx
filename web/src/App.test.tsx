import { render, screen } from "@testing-library/react";
import { App } from "./App";

test("renders the Linx wordmark and tagline", () => {
  render(<App />);
  expect(screen.getByRole("heading", { level: 1 }).textContent).toBe("linx");
  expect(screen.getByText("Your calls. Your server.")).toBeTruthy();
});
