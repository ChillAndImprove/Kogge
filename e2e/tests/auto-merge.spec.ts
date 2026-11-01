// SPDX-FileCopyrightText: 2026 The Kogge authors
// SPDX-License-Identifier: EUPL-1.2

import AxeBuilder from "@axe-core/playwright";
import type { Page } from "@playwright/test";
import { apiCall, apiGet, expect, signIn, test, uniqueKey } from "./fixtures";

/** The page passes axe's checks of WCAG 2.1 AA, in the light and the dark colour scheme. */
async function expectAccessible(page: Page, name: string) {
  for (const scheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
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
    expect(violations, `${name}, ${scheme}`).toEqual([]);
  }
  await page.emulateMedia({ colorScheme: "light" });
}

test("the merge dialog says why a pull request cannot merge yet, and merges it when ready", async ({
  page,
  browser,
  baseURL,
}) => {
  await signIn(page);
  const key = uniqueKey("M");
  const username = `rev-${key.toLowerCase()}`;
  const password = "Review-Secret-2026!";
  const reviewer = await apiCall(page, "POST", "/api/v1/admin/users", {
    username,
    displayName: "Rafael Prüfer",
    password,
    email: `${username}@example.com`,
  });
  const me = await apiGet(page, "/api/v1/me");
  await apiCall(page, "POST", "/api/v1/code/projects", {
    key,
    name: `Merge ${key}`,
  });
  await apiCall(page, "POST", `/api/v1/code/projects/${key}/repos`, {
    name: "Shop",
    readme: true,
  });
  await apiCall(page, "PUT", `/api/v1/code/projects/${key}/permissions`, [
    {
      holder: { type: "user", id: me.user.id },
      permission: "codeproject.admin",
    },
    { holder: { type: "user", id: reviewer.id }, permission: "repo.read" },
  ]);
  const repo = `/api/v1/code/repos/${key}/shop`;
  await apiCall(page, "PATCH", repo, {
    settings: {
      mergeStrategies: ["rebase", "squash", "merge"],
      requiredApprovals: 1,
      noNeedsWork: true,
      tasksResolved: true,
      requiredBuilds: 0,
      resetApprovals: false,
      deleteSourceBranch: true,
      defaultReviewers: [],
    },
  });
  await apiCall(page, "PUT", `${repo}/files`, {
    branch: "main",
    newBranch: "cart",
    path: "cart.txt",
    content: "cart\n",
    message: "Add the cart",
  });
  const pr = await apiCall(page, "POST", `${repo}/pull-requests`, {
    title: "Cart",
    source: "cart",
  });
  const prPath = `/code/${key.toLowerCase()}/shop/pull-requests/${pr.number}`;
  await page.goto(prPath);

  // The dialog explains the strategy and what keeps the pull request from merging.
  await page.getByRole("button", { name: "Merge", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(
    dialog.getByText(/applied to the target branch one by one/),
  ).toBeVisible();
  await expect(
    dialog.getByText("The commits keep their messages."),
  ).toBeVisible();
  await expect(dialog.getByText("Not mergeable yet:")).toBeVisible();
  await expect(dialog.getByText("Approvals: 0 of 1 required.")).toBeVisible();
  await expect(
    dialog.getByRole("button", { name: "Merge", exact: true }),
  ).toBeDisabled();
  await expectAccessible(page, "the merge dialog");
  // Squash writes a message, which starts as the default and can be changed.
  await dialog.getByRole("combobox", { name: "Strategy" }).click();
  await page.getByRole("option", { name: "Squash" }).click();
  await expect(
    dialog.getByText(/All changes become one new commit/),
  ).toBeVisible();
  await expect(
    dialog.getByRole("textbox", { name: "Commit message" }),
  ).toHaveValue(`Cart (#${pr.number})\n\n* Add the cart\n`);
  await dialog.getByRole("combobox", { name: "Strategy" }).click();
  await page.getByRole("option", { name: "Rebase", exact: true }).click();

  // Merged when ready: on until the approval comes, shown on the page and in the list.
  await dialog.getByRole("button", { name: "Merge when ready" }).click();
  await expect(dialog).toHaveCount(0);
  await expect(
    page.getByText(
      /turned it on \(Rebase\): the pull request merges as soon as every merge check passes/,
    ),
  ).toBeVisible();
  await expect(page.getByText("Waiting for the merge checks.")).toBeVisible();
  await expect(page.getByText(/turned on auto-merge \(Rebase\)/)).toBeVisible();
  await expectAccessible(page, "a pull request that merges when ready");
  await page.goto(`/code/${key.toLowerCase()}/shop/pull-requests`);
  await expect(
    page.getByRole("row", { name: /Cart/ }).getByText("Auto-merge"),
  ).toBeVisible();
  await page.goto(prPath);

  const context = await browser.newContext({
    baseURL,
    locale: "en-GB",
    storageState: { cookies: [], origins: [] },
  });
  const rafael = await context.newPage();
  await rafael.goto("/");
  await rafael.getByRole("textbox", { name: "User name" }).fill(username);
  await rafael.getByRole("textbox", { name: "Password" }).fill(password);
  await rafael.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(rafael.getByRole("heading", { name: /Hello/ })).toBeVisible();
  await rafael.goto(prPath);
  await rafael.getByRole("button", { name: "Approve", exact: true }).click();
  await context.close();

  // The page follows by itself.
  await expect(page.getByText("Merged", { exact: true }).first()).toBeVisible({
    timeout: 20_000,
  });
  await expect(
    page.getByText(/Auto-merge merged the pull request for .* \(Rebase\)/),
  ).toBeVisible();
  const merged = await apiGet(page, `${repo}/pull-requests/${pr.number}`);
  expect(merged.state).toBe("merged");
  const branches = await apiGet(page, `${repo}/branches`);
  expect(branches.items.map((b: { name: string }) => b.name)).toEqual(["main"]);
});
