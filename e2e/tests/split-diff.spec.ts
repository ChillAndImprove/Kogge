// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import AxeBuilder from "@axe-core/playwright";
import { apiCall, apiGet, expect, signIn, test, uniqueKey } from "./fixtures";

test("review a pull request side by side, commenting on both sides", async ({
  page,
}) => {
  await signIn(page);
  const key = uniqueKey("S");
  const me = await apiGet(page, "/api/v1/me");
  await apiCall(page, "POST", "/api/v1/code/projects", {
    key,
    name: `Split ${key}`,
  });
  await apiCall(page, "POST", `/api/v1/code/projects/${key}/repos`, {
    name: "Rates",
    readme: true,
  });
  await apiCall(page, "PUT", `/api/v1/code/projects/${key}/permissions`, [
    {
      holder: { type: "user", id: me.user.id },
      permission: "codeproject.admin",
    },
  ]);
  const repo = `/api/v1/code/repos/${key}/rates`;
  await apiCall(page, "PUT", `${repo}/files`, {
    branch: "main",
    path: "rates.go",
    content:
      "package rates\n\n// VAT is the tax rate.\nconst VAT = 16\n\nconst Reduced = 5\n",
  });
  await apiCall(page, "PUT", `${repo}/files`, {
    branch: "main",
    newBranch: "vat",
    path: "rates.go",
    content:
      "package rates\n\n// VAT is the tax rate in percent, a very long comment line that has to wrap within its half of the table.\nconst VAT = 19\n\nconst Reduced = 7\n",
  });
  const pr = await apiCall(page, "POST", `${repo}/pull-requests`, {
    title: "VAT 19",
    source: "vat",
  });
  await page.goto(
    `/code/${key.toLowerCase()}/rates/pull-requests/${pr.number}`,
  );
  await page.getByRole("tab", { name: "Changes" }).click();

  // Side by side: the old version on the left, the new one on the right, highlighted as in the unified view.
  await page.getByText("Side by side", { exact: true }).click();
  const split = page.locator("table.kogge-split");
  await expect(split).toBeVisible();
  const changed = split
    .locator("tr", { has: page.locator("td.src.del") })
    .filter({ hasText: "const VAT = 16" });
  await expect(changed.locator("td.src.del")).toContainText("const VAT = 16");
  await expect(changed.locator("td.src.add")).toContainText("const VAT = 19");
  await expect(split.locator("td.src.add .kd").first()).toHaveText("const");

  // An added line is commented on the right (and a change of it can be suggested), a removed one on the left.
  await split.getByRole("button", { name: "Comment on line 4" }).click();
  await page.getByRole("button", { name: "Suggest a change" }).click();
  await expect(
    page.getByRole("textbox", { name: "Add a comment" }).first(),
  ).toHaveValue("```suggestion\nconst VAT = 19\n```\n");
  await page
    .getByRole("textbox", { name: "Add a comment" })
    .first()
    .fill("Nineteen, as of July");
  await page
    .getByRole("button", { name: "Comment", exact: true })
    .first()
    .click();
  const comments = split.locator("tr.comments");
  await expect(comments.locator("td").nth(1)).toContainText(
    "Nineteen, as of July",
  );
  await expect(comments.locator("td").nth(0)).not.toContainText("Nineteen");
  // By keyboard: the number of the removed line takes the focus and opens the form.
  const removed = split.getByRole("button", {
    name: "Comment on removed line 6",
  });
  await removed.focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("button", { name: "Suggest a change" }),
  ).toHaveCount(0);
  await page
    .getByRole("textbox", { name: "Add a comment" })
    .first()
    .fill("Why five before?");
  await page
    .getByRole("button", { name: "Comment", exact: true })
    .first()
    .click();
  const left = split
    .locator("tr.comments")
    .filter({ hasText: "Why five before?" });
  await expect(left.locator("td").nth(0)).toContainText("Why five before?");

  // The choice is kept; one file can be shown the other way.
  await page.reload();
  await page.getByRole("tab", { name: "Changes" }).click();
  await expect(page.locator("table.kogge-split")).toBeVisible();
  await page.getByRole("button", { name: "Show this file unified" }).click();
  await expect(page.locator("table.kogge-split")).toHaveCount(0);
  await expect(
    page.locator("table.kogge-code").getByText("Nineteen, as of July"),
  ).toBeVisible();
  await expect(
    page.locator("table.kogge-code").getByText("Why five before?"),
  ).toBeVisible();
  await page.getByText("Unified", { exact: true }).click();
  await expect(page.locator("table.kogge-split")).toHaveCount(0);

  // The comments went where the unified view anchors them.
  const stored = await apiGet(
    page,
    `${repo}/pull-requests/${pr.number}/timeline`,
  );
  const lines = stored
    .filter((a: { comment?: { line?: number } }) => a.comment?.line)
    .map(
      (a: { comment: { side: string; line: number } }) =>
        `${a.comment.side}:${a.comment.line}`,
    )
    .sort();
  expect(lines).toEqual(["new:4", "old:6"]);
});

test("the changes meet WCAG 2.1 AA in both layouts and colour schemes", async ({
  page,
}) => {
  await signIn(page);
  const key = uniqueKey("W");
  const me = await apiGet(page, "/api/v1/me");
  await apiCall(page, "POST", "/api/v1/code/projects", {
    key,
    name: `Access ${key}`,
  });
  await apiCall(page, "POST", `/api/v1/code/projects/${key}/repos`, {
    name: "App",
    readme: true,
  });
  await apiCall(page, "PUT", `/api/v1/code/projects/${key}/permissions`, [
    {
      holder: { type: "user", id: me.user.id },
      permission: "codeproject.admin",
    },
  ]);
  const repo = `/api/v1/code/repos/${key}/app`;
  await apiCall(page, "PUT", `${repo}/files`, {
    branch: "main",
    path: "a.go",
    content: "package a\n\nconst A = 1\n",
  });
  await apiCall(page, "PUT", `${repo}/files`, {
    branch: "main",
    newBranch: "b",
    path: "a.go",
    content: "package a\n\nconst A = 2\nconst B = 3\n",
  });
  const pr = await apiCall(page, "POST", `${repo}/pull-requests`, {
    title: "Two",
    source: "b",
  });
  await apiCall(page, "POST", `${repo}/pull-requests/${pr.number}/comments`, {
    path: "a.go",
    line: 3,
    side: "old",
    markdown: "Was one",
  });
  await page.goto(`/code/${key.toLowerCase()}/app/pull-requests/${pr.number}`);
  await page.getByRole("tab", { name: "Changes" }).click();
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    for (const layout of ["Unified", "Side by side"]) {
      await page.getByText(layout, { exact: true }).click();
      await expect(page.getByText("Was one")).toBeVisible();
      await page.waitForLoadState("networkidle");
      const result = await new AxeBuilder({ page })
        .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
        .analyze();
      const violations = result.violations.map(
        (v) =>
          `${v.impact} ${v.id}: ${v.nodes
            .map((n) => n.target.join(" "))
            .slice(0, 3)
            .join(" | ")}`,
      );
      expect(violations, `${layout}, ${scheme}`).toEqual([]);
    }
  }
});

test("narrow screens show changes one below the other", async ({ page }) => {
  await signIn(page);
  const key = uniqueKey("N");
  const me = await apiGet(page, "/api/v1/me");
  await apiCall(page, "POST", "/api/v1/code/projects", {
    key,
    name: `Narrow ${key}`,
  });
  await apiCall(page, "POST", `/api/v1/code/projects/${key}/repos`, {
    name: "App",
    readme: true,
  });
  await apiCall(page, "PUT", `/api/v1/code/projects/${key}/permissions`, [
    {
      holder: { type: "user", id: me.user.id },
      permission: "codeproject.admin",
    },
  ]);
  const repo = `/api/v1/code/repos/${key}/app`;
  await apiCall(page, "PUT", `${repo}/files`, {
    branch: "main",
    newBranch: "b",
    path: "README.md",
    content: "# App\n\nMore.\n",
  });
  const pr = await apiCall(page, "POST", `${repo}/pull-requests`, {
    title: "More",
    source: "b",
  });
  await page.goto(`/code/${key.toLowerCase()}/app/pull-requests/${pr.number}`);
  await page.getByRole("tab", { name: "Changes" }).click();
  await page.getByText("Side by side", { exact: true }).click();
  await expect(page.locator("table.kogge-split")).toBeVisible();
  await page.setViewportSize({ width: 390, height: 800 });
  await expect(page.locator("table.kogge-split")).toHaveCount(0);
  await expect(page.locator("table.kogge-code")).toContainText("More.");
  await expect(page.getByText("Side by side", { exact: true })).toHaveCount(0);
  // Wide again: the reader's choice is back.
  await page.setViewportSize({ width: 1280, height: 800 });
  await expect(page.locator("table.kogge-split")).toBeVisible();
});
