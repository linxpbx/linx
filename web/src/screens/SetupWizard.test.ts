import { describe, expect, it } from "vitest";
import { extensionProblem, rowProblems } from "@/screens/SetupWizard";

// Found in the install demo: the People step said nothing about a wrong
// extension or email until "Create".
const people = { from: 100, to: 599 };
const taken = new Set(["100"]);

describe("extensionProblem", () => {
  it("says what's wrong as it's typed", () => {
    expect(extensionProblem("1234", 3, people, taken)).toBe("Extensions have 3 digits.");
    expect(extensionProblem("12a", 3, people, taken)).toBe("Digits only.");
    expect(extensionProblem("650", 3, people, taken)).toBe("Extensions are 100–599.");
    expect(extensionProblem("100", 3, people, taken)).toBe("100 is already used.");
    expect(extensionProblem("", 3, people, taken)).toBe("Give an extension number.");
    expect(extensionProblem("101", 3, people, taken)).toBe("");
  });
});

describe("rowProblems", () => {
  it("ignores an empty row and checks a filled one", () => {
    expect(rowProblems({ name: "", email: "", role: "user", number: "101" }, 3, people, taken)).toEqual({});
    expect(rowProblems({ name: "Sara", email: "sara@", role: "user", number: "1011" }, 3, people, taken)).toEqual({
      email: "That doesn't look like an email address.",
      number: "Extensions have 3 digits.",
    });
    expect(rowProblems({ name: "", email: "sara@example.com", role: "user", number: "101" }, 3, people, taken)).toEqual({
      name: "Give their name.",
    });
    // A person needs an email; a Phone (a door phone: the owner's example) doesn't.
    expect(rowProblems({ name: "Sara", email: "", role: "admin", number: "101" }, 3, people, taken)).toEqual({
      email: "A person needs an email for their invite link. For a door phone or a room, choose Phone.",
    });
    expect(rowProblems({ name: "Front door", email: "", role: "phone", number: "150" }, 3, people, taken)).toEqual({});
    expect(rowProblems({ name: "Sara", email: "sara@example.com", role: "user", number: "101" }, 3, people, taken)).toEqual({});
  });
});
